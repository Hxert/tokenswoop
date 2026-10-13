// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/// @notice Principal is directly withdrawable except for explicitly reported debt.
/// @dev Operator reports are trusted offchain accounting, not proof of usage. The
/// manager may release debt and pause deposits, but cannot report debt or take principal.
/// Neither role can prevent withdrawal of available principal through this contract.
contract TokenSwoopReserveVault is ReentrancyGuard {
    using SafeERC20 for IERC20;

    struct Debt { uint256 amount; uint256 epoch; bool reported; }
    IERC20 public immutable reserveToken;
    address public immutable settlementOperator;
    address public immutable manager;
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

    event Deposited(address indexed user, uint256 amount, uint256 principal);
    event Withdrawn(address indexed user, uint256 amount, uint256 principal);
    event DebtReported(address indexed user, uint256 indexed epoch, bytes32 reportHash);
    event DebtCleared(address indexed user, uint256 epoch, uint256 releasedAmount);
    event DepositsPausedChanged(bool paused);

    constructor(address token, address manager_, address operator) {
        if (token.code.length == 0 || manager_ == address(0) || operator == address(0)
            || manager_ == operator) revert InvalidConfiguration();
        if (IERC20Metadata(token).decimals() != 6) revert InvalidConfiguration();
        reserveToken = IERC20(token);
        manager = manager_;
        settlementOperator = operator;
    }

    /// @notice All reserve assets remain in this contract. There is no lending or admin asset exit.
    function custodyMode() external pure returns (bytes32) { return keccak256("vault"); }

    function deposit(uint256 amount) external nonReentrant {
        if (paused) revert DepositsPaused();
        if (amount == 0) revert ZeroAmount();
        uint256 beforeBalance = reserveToken.balanceOf(address(this));
        reserveToken.safeTransferFrom(msg.sender, address(this), amount);
        if (reserveToken.balanceOf(address(this)) - beforeBalance != amount) revert UnsupportedTokenTransfer();
        principalOf[msg.sender] += amount;
        totalPrincipal += amount;
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
        reserveToken.safeTransfer(msg.sender, amount);
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

    function totalAssets() public view returns (uint256) {
        return reserveToken.balanceOf(address(this));
    }

    function _assertBacking() internal view {
        if (totalAssets() < totalPrincipal) revert InsufficientBacking();
    }
}
