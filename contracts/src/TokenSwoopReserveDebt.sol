// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {IAaveV3Pool, IAaveAToken} from "./interfaces/IAaveV3Pool.sol";

/// @notice Principal is directly withdrawable except for explicitly reported debt.
/// @dev Operator reports are trusted offchain accounting, not proof of usage. The
/// manager may release debt and pause deposits, but cannot report debt or take principal.
/// Neither role can prevent withdrawal of available principal through this contract.
contract TokenSwoopReserveDebt is ReentrancyGuard {
    using SafeERC20 for IERC20;

    struct Debt { uint256 amount; uint256 epoch; bool reported; }
    IERC20 public immutable reserveToken;
    IAaveV3Pool public immutable aavePool;
    IERC20 public immutable aToken;
    address public immutable treasury;
    address public immutable settlementOperator;
    address public immutable manager;
    uint256 public constant YIELD_BUFFER = 1e6;
    uint256 public totalPrincipal;
    bool public paused;
    mapping(address => uint256) public principalOf;
    mapping(address => uint256) public debtOf;
    mapping(address => uint256) public debtEpoch;
    mapping(address => mapping(bytes32 => Debt)) public debts;

    error InvalidConfiguration();
    error ZeroAmount();
    error NotOperator();
    error NotManager();
    error DepositsPaused();
    error InvalidDebtReport();
    error StaleDebtEpoch();
    error DebtReleased(bytes32 id);
    error InsufficientWithdrawable(uint256 requested, uint256 available);
    error UnsupportedTokenTransfer();
    error InsufficientBacking();
    error UnexpectedPoolWithdrawal();
    error ExcessYieldWithdrawal();

    event Deposited(address indexed user, uint256 amount, uint256 principal);
    event Withdrawn(address indexed user, uint256 amount, uint256 principal);
    event DebtReported(address indexed user, uint256 indexed epoch, bytes32 reportHash);
    event DebtCleared(address indexed user, uint256 epoch, uint256 releasedAmount);
    event DepositsPausedChanged(bool paused);
    event BufferFunded(address indexed funder, uint256 amount);
    event YieldWithdrawn(address indexed treasury, uint256 amount);

    constructor(address token, address pool, address manager_, address operator, address treasury_) {
        if (token.code.length == 0 || pool.code.length == 0 || manager_ == address(0)
            || operator == address(0) || manager_ == operator || treasury_ == address(0)
            || treasury_ == address(this)) revert InvalidConfiguration();
        if (IERC20Metadata(token).decimals() != 6) revert InvalidConfiguration();
        address interest = IAaveV3Pool(pool).getReserveAToken(token);
        if (interest.code.length == 0 || IAaveAToken(interest).POOL() != pool
            || IAaveAToken(interest).UNDERLYING_ASSET_ADDRESS() != token
            || IERC20Metadata(interest).decimals() != 6) revert InvalidConfiguration();
        reserveToken = IERC20(token);
        aavePool = IAaveV3Pool(pool);
        aToken = IERC20(interest);
        manager = manager_;
        settlementOperator = operator;
        treasury = treasury_;
    }

    function deposit(uint256 amount) external nonReentrant {
        if (paused) revert DepositsPaused();
        if (amount == 0) revert ZeroAmount();
        uint256 beforeBalance = reserveToken.balanceOf(address(this));
        reserveToken.safeTransferFrom(msg.sender, address(this), amount);
        if (reserveToken.balanceOf(address(this)) - beforeBalance != amount) revert UnsupportedTokenTransfer();
        principalOf[msg.sender] += amount;
        totalPrincipal += amount;
        reserveToken.forceApprove(address(aavePool), amount);
        aavePool.supply(address(reserveToken), amount, address(this), 0);
        reserveToken.forceApprove(address(aavePool), 0);
        _assertBacking();
        emit Deposited(msg.sender, amount, principalOf[msg.sender]);
    }

    function frozenOf(address user) public view returns (uint256) {
        return debtOf[user] < principalOf[user] ? debtOf[user] : principalOf[user];
    }

    function withdrawableOf(address user) public view returns (uint256) {
        return principalOf[user] - frozenOf(user);
    }

    /// @notice Direct withdrawal to the caller; no platform approval or waiting period.
    function withdraw(uint256 amount) external nonReentrant {
        if (amount == 0) revert ZeroAmount();
        uint256 available = withdrawableOf(msg.sender);
        if (amount > available) revert InsufficientWithdrawable(amount, available);
        principalOf[msg.sender] -= amount;
        totalPrincipal -= amount;
        _payUnderlying(msg.sender, amount);
        _assertBacking();
        emit Withdrawn(msg.sender, amount, principalOf[msg.sender]);
    }

    /// @notice Sets remaining confirmed debt per immutable API request ID. Zero releases paid debt.
    /// IDs must be sorted and unique. Cleared IDs cannot be reused in a later management epoch.
    function reportDebts(address user, bytes32[] calldata ids, uint256[] calldata amounts, uint256 expectedEpoch)
        external nonReentrant
    {
        if (msg.sender != settlementOperator) revert NotOperator();
        if (user == address(0) || ids.length == 0 || ids.length > 64 || ids.length != amounts.length)
            revert InvalidDebtReport();
        if (expectedEpoch != debtEpoch[user]) revert StaleDebtEpoch();
        uint256 total = debtOf[user];
        for (uint256 i; i < ids.length; ++i) {
            if (ids[i] == bytes32(0) || (i > 0 && ids[i] <= ids[i-1])) revert InvalidDebtReport();
            Debt storage debt = debts[user][ids[i]];
            if (debt.reported && debt.epoch != expectedEpoch) revert DebtReleased(ids[i]);
            total = total - debt.amount + amounts[i];
            debt.amount = amounts[i];
            debt.epoch = expectedEpoch;
            debt.reported = true;
        }
        debtOf[user] = total;
        emit DebtReported(user, expectedEpoch, keccak256(msg.data));
    }

    /// @notice Releases all current freezes; old request IDs stay released permanently.
    function clearDebt(address user) external nonReentrant {
        if (msg.sender != manager) revert NotManager();
        if (user == address(0)) revert InvalidConfiguration();
        uint256 released = debtOf[user];
        debtOf[user] = 0;
        ++debtEpoch[user];
        emit DebtCleared(user, debtEpoch[user], released);
    }

    function setDepositsPaused(bool value) external {
        if (msg.sender != manager) revert NotManager();
        paused = value;
        emit DepositsPausedChanged(value);
    }

    function fundBuffer(uint256 amount) external nonReentrant {
        if (amount == 0) revert ZeroAmount();
        uint256 beforeBalance = reserveToken.balanceOf(address(this));
        reserveToken.safeTransferFrom(msg.sender, address(this), amount);
        if (reserveToken.balanceOf(address(this)) - beforeBalance != amount) revert UnsupportedTokenTransfer();
        emit BufferFunded(msg.sender, amount);
    }

    function totalAssets() public view returns (uint256) {
        return reserveToken.balanceOf(address(this)) + aToken.balanceOf(address(this));
    }

    function availableYield() public view returns (uint256) {
        uint256 assets = totalAssets();
        if (assets <= totalPrincipal + YIELD_BUFFER) return 0;
        return assets - totalPrincipal - YIELD_BUFFER;
    }

    function withdrawYield(uint256 amount) external nonReentrant {
        if (msg.sender != manager) revert NotManager();
        if (amount == 0) revert ZeroAmount();
        if (amount > availableYield()) revert ExcessYieldWithdrawal();
        _payUnderlying(treasury, amount);
        _assertBacking();
        emit YieldWithdrawn(treasury, amount);
    }

    function _payUnderlying(address to, uint256 amount) internal {
        uint256 idle = reserveToken.balanceOf(address(this));
        if (idle < amount && aavePool.withdraw(address(reserveToken), amount-idle, address(this)) != amount-idle)
            revert UnexpectedPoolWithdrawal();
        reserveToken.safeTransfer(to, amount);
    }

    function _assertBacking() internal view {
        if (totalAssets() < totalPrincipal) revert InsufficientBacking();
    }
}
