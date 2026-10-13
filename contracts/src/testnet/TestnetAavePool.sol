// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Math} from "@openzeppelin/contracts/utils/math/Math.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {Ownable2Step} from "@openzeppelin/contracts/access/Ownable2Step.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {IAaveV3Pool} from "../interfaces/IAaveV3Pool.sol";

/// @notice TEST DOUBLE ONLY. Not an Aave deployment, lending market or yield guarantee.
/// @dev Exercises Aave v3 supply/withdraw selectors, index rounding, funded yield and illiquidity.
///      No borrowing, risk engine, governance or automatic interest accrual is implemented.
contract TestnetAavePool is IAaveV3Pool, Ownable2Step, Pausable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    uint256 public constant RAY = 1e27;
    IERC20 public immutable underlying;
    TestnetAToken public immutable receiptToken;
    uint256 public liquidityIndex = RAY;
    uint256 public withdrawalLimit = type(uint256).max;

    error TestNetworkOnly(uint256 chainId);
    error InvalidAsset();
    error InvalidAmount();
    error InsufficientLiquidity();
    error NoSuppliers();
    error InexactTransfer();

    event Supply(address indexed asset, address indexed supplier, address indexed onBehalfOf, uint256 amount);
    event Withdraw(address indexed asset, address indexed supplier, address indexed to, uint256 amount);
    event TestYieldFunded(uint256 amount, uint256 liquidityIndex);
    event TestWithdrawalLimitChanged(uint256 amount);

    constructor(address token, address initialOwner) Ownable(initialOwner) {
        if (block.chainid != 10143 && block.chainid != 31337) revert TestNetworkOnly(block.chainid);
        if (token.code.length == 0 || IERC20Metadata(token).decimals() != 6) revert InvalidAsset();
        underlying = IERC20(token);
        receiptToken = new TestnetAToken(address(this), token);
    }

    function getReserveAToken(address asset) external view returns (address) {
        return asset == address(underlying) ? address(receiptToken) : address(0);
    }

    function getReserveNormalizedIncome(address asset) external view returns (uint256) {
        if (asset != address(underlying)) revert InvalidAsset();
        return liquidityIndex;
    }

    function supply(address asset, uint256 amount, address onBehalfOf, uint16) external nonReentrant whenNotPaused {
        if (asset != address(underlying) || onBehalfOf == address(0)) revert InvalidAsset();
        uint256 scaled = Math.mulDiv(amount, RAY, liquidityIndex);
        if (scaled == 0) revert InvalidAmount();
        uint256 beforeBalance = underlying.balanceOf(address(receiptToken));
        underlying.safeTransferFrom(msg.sender, address(receiptToken), amount);
        if (underlying.balanceOf(address(receiptToken)) - beforeBalance != amount) revert InexactTransfer();
        receiptToken.mintScaled(onBehalfOf, scaled, amount);
        emit Supply(asset, msg.sender, onBehalfOf, amount);
    }

    function withdraw(address asset, uint256 amount, address to) external nonReentrant whenNotPaused returns (uint256) {
        if (asset != address(underlying) || to == address(0) || to == address(receiptToken)) revert InvalidAsset();
        uint256 balance = receiptToken.balanceOf(msg.sender);
        if (amount == type(uint256).max) amount = balance;
        if (amount == 0 || amount > balance) revert InvalidAmount();
        if (amount > withdrawalLimit || amount > underlying.balanceOf(address(receiptToken))) {
            revert InsufficientLiquidity();
        }
        uint256 scaled = amount == balance
            ? receiptToken.scaledBalanceOf(msg.sender)
            : Math.mulDiv(amount, RAY, liquidityIndex, Math.Rounding.Ceil);
        receiptToken.burnScaled(msg.sender, scaled, amount);
        receiptToken.transferUnderlyingTo(to, amount);
        emit Withdraw(asset, msg.sender, to, amount);
        return amount;
    }

    /// @notice Adds real test tokens to the mock pool before increasing suppliers' index.
    function accrueYield(uint256 amount) external onlyOwner nonReentrant whenNotPaused {
        if (amount == 0) revert InvalidAmount();
        uint256 scaledSupply = receiptToken.scaledTotalSupply();
        if (scaledSupply == 0) revert NoSuppliers();
        underlying.safeTransferFrom(msg.sender, address(receiptToken), amount);
        liquidityIndex += Math.mulDiv(amount, RAY, scaledSupply);
        emit TestYieldFunded(amount, liquidityIndex);
    }

    /// @notice Test control: zero blocks pool redemptions without changing aToken balances.
    function setWithdrawalLimit(uint256 amount) external onlyOwner {
        withdrawalLimit = amount;
        emit TestWithdrawalLimitChanged(amount);
    }

    function pause() external onlyOwner {
        _pause();
    }

    function unpause() external onlyOwner {
        _unpause();
    }
}

/// @notice Minimal rebasing receipt used only by TestnetAavePool.
contract TestnetAToken is IERC20Metadata {
    using SafeERC20 for IERC20;
    string public constant name = "TokenSwoop Mock Aave kUSDC";
    string public constant symbol = "akUSDC";
    uint8 public constant decimals = 6;
    address public immutable POOL;
    address public immutable UNDERLYING_ASSET_ADDRESS;
    mapping(address => uint256) private _scaledBalances;
    mapping(address => mapping(address => uint256)) public allowance;
    uint256 public scaledTotalSupply;

    error OnlyPool();
    error InvalidRecipient();
    error InsufficientBalance();
    error InsufficientAllowance();

    constructor(address pool, address token) {
        POOL = pool;
        UNDERLYING_ASSET_ADDRESS = token;
    }

    modifier onlyPool() {
        if (msg.sender != POOL) revert OnlyPool();
        _;
    }

    function scaledBalanceOf(address user) external view returns (uint256) {
        return _scaledBalances[user];
    }

    function totalSupply() external view returns (uint256) {
        return _value(scaledTotalSupply);
    }

    function balanceOf(address user) public view returns (uint256) {
        return _value(_scaledBalances[user]);
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        emit Approval(msg.sender, spender, amount);
        return true;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        _transfer(msg.sender, to, amount);
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        uint256 approved = allowance[from][msg.sender];
        if (approved < amount) revert InsufficientAllowance();
        if (approved != type(uint256).max) allowance[from][msg.sender] = approved - amount;
        _transfer(from, to, amount);
        return true;
    }

    function mintScaled(address user, uint256 scaled, uint256 amount) external onlyPool {
        _scaledBalances[user] += scaled;
        scaledTotalSupply += scaled;
        emit Transfer(address(0), user, amount);
    }

    function burnScaled(address user, uint256 scaled, uint256 amount) external onlyPool {
        if (scaled > _scaledBalances[user]) revert InsufficientBalance();
        _scaledBalances[user] -= scaled;
        scaledTotalSupply -= scaled;
        emit Transfer(user, address(0), amount);
    }

    function transferUnderlyingTo(address to, uint256 amount) external onlyPool {
        IERC20(UNDERLYING_ASSET_ADDRESS).safeTransfer(to, amount);
    }

    function _value(uint256 scaled) internal view returns (uint256) {
        return Math.mulDiv(scaled, TestnetAavePool(POOL).liquidityIndex(), 1e27);
    }

    function _transfer(address from, address to, uint256 amount) internal {
        if (to == address(0)) revert InvalidRecipient();
        uint256 scaled = Math.mulDiv(amount, 1e27, TestnetAavePool(POOL).liquidityIndex(), Math.Rounding.Ceil);
        if (scaled > _scaledBalances[from]) revert InsufficientBalance();
        _scaledBalances[from] -= scaled;
        _scaledBalances[to] += scaled;
        emit Transfer(from, to, amount);
    }
}
