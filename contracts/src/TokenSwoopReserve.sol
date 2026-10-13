// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {Ownable2Step} from "@openzeppelin/contracts/access/Ownable2Step.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {IAaveV3Pool, IAaveAToken} from "./interfaces/IAaveV3Pool.sol";

/// @notice User-owned USDC reserve principal and settlement-gated withdrawals.
/// @dev The settlement operator attests to OFFCHAIN fee clearance. The contract cannot prove
///      that all inference requests were billed. The gateway must stop accepting new requests
///      while a withdrawal is active, and drain all in-flight work before approving it.
///      Providers independently apply their own offchain tier policies to principalOf(user).
contract TokenSwoopReserve is Ownable2Step, Pausable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    struct WithdrawalRequest {
        uint256 requestId;
        uint256 amount;
        uint256 operatorVersion;
        uint64 requestedAt;
        uint64 approvedUntil;
        bool active;
    }

    IERC20 public immutable reserveToken;
    IAaveV3Pool public immutable aavePool;
    IERC20 public immutable aToken;
    address public immutable treasury;
    // Surplus below this amount is retained to absorb Aave's integer rounding. Funded by
    // the platform, never deducted from user deposits. It is not an insurance guarantee.
    uint256 public constant YIELD_BUFFER = 1e6;

    address public settlementOperator;
    uint256 public operatorVersion = 1;
    uint256 public totalPrincipal;
    mapping(address user => uint256) public principalOf;
    mapping(address user => WithdrawalRequest) private _withdrawals;

    error InvalidToken(address token);
    error UnsupportedDecimals(uint8 decimals);
    error InvalidPool(address pool);
    error InvalidAToken(address token);
    error InvalidAddress();
    error ZeroAmount();
    error UnsupportedTokenTransfer();
    error InsufficientPrincipal(uint256 requested, uint256 available);
    error WithdrawalAlreadyPending();
    error WithdrawalNotPending();
    error WithdrawalVersionMismatch(uint256 expected, uint256 supplied);
    error WithdrawalNotApproved();
    error ApprovalExpired(uint64 approvedUntil);
    error StaleOperatorApproval();
    error NotSettlementOperator();
    error InvalidApprovalExpiry();
    error InsufficientBacking(uint256 assets, uint256 principal);
    error ExcessYieldWithdrawal(uint256 requested, uint256 available);
    error UnexpectedPoolWithdrawal(uint256 requested, uint256 returnedAmount);

    event Deposited(address indexed user, uint256 amount, uint256 principal);
    event BufferFunded(address indexed funder, uint256 amount);
    event WithdrawalRequested(address indexed user, uint256 indexed requestId, uint256 amount);
    event WithdrawalApproved(
        address indexed user,
        uint256 indexed requestId,
        address indexed operator,
        uint256 operatorVersion,
        uint64 approvedUntil
    );
    event WithdrawalApprovalCleared(address indexed user, uint256 indexed requestId, bytes32 reason);
    event WithdrawalCancelled(address indexed user, uint256 indexed requestId);
    event Withdrawn(address indexed user, uint256 indexed requestId, uint256 amount, uint256 principal);
    event SettlementOperatorChanged(address indexed previous, address indexed current, uint256 version);
    event YieldWithdrawn(address indexed treasury, uint256 amount);

    /// @param pool A fixed Aave v3.5+ pool, or the explicitly labelled testnet mock.
    /// @dev Pool/token/treasury cannot be changed after deployment.
    constructor(address token, address pool, address initialOwner, address operator, address treasury_)
        Ownable(initialOwner)
    {
        if (token == address(0) || token.code.length == 0) revert InvalidToken(token);
        uint8 decimals = IERC20Metadata(token).decimals();
        if (decimals != 6) revert UnsupportedDecimals(decimals);
        if (operator == address(0) || treasury_ == address(0) || treasury_ == address(this)) revert InvalidAddress();
        reserveToken = IERC20(token);
        aavePool = IAaveV3Pool(pool);
        if (pool.code.length == 0) revert InvalidPool(pool);
        address interestToken = IAaveV3Pool(pool).getReserveAToken(token);
        if (interestToken == address(0) || interestToken.code.length == 0) revert InvalidAToken(interestToken);
        if (
            IAaveAToken(interestToken).POOL() != pool || IAaveAToken(interestToken).UNDERLYING_ASSET_ADDRESS() != token
                || IERC20Metadata(interestToken).decimals() != 6
        ) revert InvalidAToken(interestToken);
        aToken = IERC20(interestToken);
        treasury = treasury_;
        settlementOperator = operator;
    }

    /// @notice Deposits principal belonging to msg.sender. Does not grant any PayG allowance.
    function deposit(uint256 amount) external nonReentrant whenNotPaused {
        if (amount == 0) revert ZeroAmount();
        if (_withdrawals[msg.sender].active) revert WithdrawalAlreadyPending();
        uint256 beforeBalance = reserveToken.balanceOf(address(this));
        reserveToken.safeTransferFrom(msg.sender, address(this), amount);
        if (reserveToken.balanceOf(address(this)) - beforeBalance != amount) revert UnsupportedTokenTransfer();
        principalOf[msg.sender] += amount;
        totalPrincipal += amount;
        // Exact temporary approval avoids leaving the pool access to idle buffer funds.
        reserveToken.forceApprove(address(aavePool), amount);
        aavePool.supply(address(reserveToken), amount, address(this), 0);
        reserveToken.forceApprove(address(aavePool), 0);
        _assertBacking();
        emit Deposited(msg.sender, amount, principalOf[msg.sender]);
    }

    /// @notice Adds platform-owned surplus to absorb pool rounding or an asset shortfall.
    /// @dev Does not create a refundable user principal balance.
    function fundBuffer(uint256 amount) external nonReentrant {
        if (amount == 0) revert ZeroAmount();
        uint256 beforeBalance = reserveToken.balanceOf(address(this));
        reserveToken.safeTransferFrom(msg.sender, address(this), amount);
        if (reserveToken.balanceOf(address(this)) - beforeBalance != amount) revert UnsupportedTokenTransfer();
        emit BufferFunded(msg.sender, amount);
    }

    /// @notice Begins the gateway's API freeze and fee-clearance process. Available when paused.
    function requestWithdrawal(uint256 amount) external nonReentrant returns (uint256 requestId) {
        if (amount == 0) revert ZeroAmount();
        uint256 principal = principalOf[msg.sender];
        if (amount > principal) revert InsufficientPrincipal(amount, principal);
        WithdrawalRequest storage request = _withdrawals[msg.sender];
        if (request.active) revert WithdrawalAlreadyPending();
        requestId = request.requestId + 1;
        request.requestId = requestId;
        request.amount = amount;
        request.operatorVersion = 0;
        request.requestedAt = uint64(block.timestamp);
        request.approvedUntil = 0;
        request.active = true;
        emit WithdrawalRequested(msg.sender, requestId, amount);
    }

    /// @notice Attests all fees (across all providers) are settled and all in-flight API work drained.
    /// @dev The gateway must keep API usage frozen until this request is withdrawn or cancelled.
    function approveWithdrawal(address user, uint256 requestId, uint64 approvedUntil) external nonReentrant {
        _checkOperator();
        WithdrawalRequest storage request = _currentRequest(user, requestId);
        if (approvedUntil <= block.timestamp) revert InvalidApprovalExpiry();
        request.operatorVersion = operatorVersion;
        request.approvedUntil = approvedUntil;
        emit WithdrawalApproved(user, requestId, msg.sender, operatorVersion, approvedUntil);
    }

    function clearWithdrawalApproval(address user, uint256 requestId, bytes32 reason) external nonReentrant {
        _checkOperator();
        WithdrawalRequest storage request = _currentRequest(user, requestId);
        request.approvedUntil = 0;
        emit WithdrawalApprovalCleared(user, requestId, reason);
    }

    /// @notice Cancels both the pending request and any approval. Always available.
    function cancelWithdrawal() external nonReentrant {
        WithdrawalRequest storage request = _withdrawals[msg.sender];
        if (!request.active) revert WithdrawalNotPending();
        request.active = false;
        request.approvedUntil = 0;
        emit WithdrawalCancelled(msg.sender, request.requestId);
    }

    /// @notice Redeems only the approved principal, only into the requesting user's wallet.
    /// @dev A pool/token failure rolls back principal and approval; the SAME request can be retried.
    function withdraw(uint256 requestId) external nonReentrant {
        WithdrawalRequest storage request = _currentRequest(msg.sender, requestId);
        if (request.approvedUntil == 0) revert WithdrawalNotApproved();
        if (request.operatorVersion != operatorVersion) revert StaleOperatorApproval();
        if (block.timestamp >= request.approvedUntil) revert ApprovalExpired(request.approvedUntil);
        uint256 amount = request.amount;
        principalOf[msg.sender] -= amount;
        totalPrincipal -= amount;
        request.active = false;
        request.approvedUntil = 0;
        _payUnderlying(msg.sender, amount);
        _assertBacking();
        emit Withdrawn(msg.sender, requestId, amount, principalOf[msg.sender]);
    }

    function getWithdrawal(address user) external view returns (WithdrawalRequest memory) {
        return _withdrawals[user];
    }

    /// @notice The gateway must reject new API work when true, including after approval expires.
    function isWithdrawalPending(address user) external view returns (bool) {
        return _withdrawals[user].active;
    }

    function setSettlementOperator(address operator) external onlyOwner {
        if (operator == address(0)) revert InvalidAddress();
        address previous = settlementOperator;
        settlementOperator = operator;
        ++operatorVersion; // Invalidates every outstanding approval, even if the address is reused.
        emit SettlementOperatorChanged(previous, operator, operatorVersion);
    }

    function totalAssets() public view returns (uint256) {
        uint256 assets = reserveToken.balanceOf(address(this));
        assets += aToken.balanceOf(address(this));
        return assets;
    }

    /// @notice Accounting surplus, not a guarantee of pool liquidity or positive interest.
    function availableYield() public view returns (uint256) {
        uint256 assets = totalAssets();
        if (assets <= totalPrincipal) return 0;
        uint256 surplus = assets - totalPrincipal;
        return surplus > YIELD_BUFFER ? surplus - YIELD_BUFFER : 0;
    }

    /// @notice The first implementation allocates surplus yield to the fixed platform treasury.
    /// @dev Cannot spend principal. Aave integer rounding is checked after redemption as well.
    function withdrawYield(uint256 amount) external nonReentrant onlyOwner whenNotPaused {
        if (amount == 0) revert ZeroAmount();
        uint256 available = availableYield();
        if (amount > available) revert ExcessYieldWithdrawal(amount, available);
        _payUnderlying(treasury, amount);
        _assertBacking();
        emit YieldWithdrawn(treasury, amount);
    }

    // Pausing blocks deposits and platform yield collection; user exit management stays available.
    function pause() external onlyOwner {
        _pause();
    }

    function unpause() external onlyOwner {
        _unpause();
    }

    function _currentRequest(address user, uint256 requestId)
        internal
        view
        returns (WithdrawalRequest storage request)
    {
        request = _withdrawals[user];
        if (!request.active) revert WithdrawalNotPending();
        if (request.requestId != requestId) revert WithdrawalVersionMismatch(request.requestId, requestId);
    }

    function _checkOperator() internal view {
        if (msg.sender != settlementOperator) revert NotSettlementOperator();
    }

    function _payUnderlying(address to, uint256 amount) internal {
        uint256 idle = reserveToken.balanceOf(address(this));
        if (idle < amount) {
            uint256 needed = amount - idle;
            uint256 redeemed = aavePool.withdraw(address(reserveToken), needed, address(this));
            if (redeemed != needed) revert UnexpectedPoolWithdrawal(needed, redeemed);
        }
        reserveToken.safeTransfer(to, amount);
    }

    function _assertBacking() internal view {
        uint256 assets = totalAssets();
        if (assets < totalPrincipal) revert InsufficientBacking(assets, totalPrincipal);
    }
}
