// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/// @notice Non-custodial USDC bill payments for one immutable provider.
/// @dev A bill covers one or more offchain requests. The token's allowance is the only spending limit;
///      approval does not lock funds or prove service delivery.
contract TokenSwoopPayG is Ownable, Pausable, ReentrancyGuard {
    using SafeERC20 for IERC20;

    IERC20 public immutable paymentToken;
    address public immutable provider;
    address public immutable recipient;

    // Paid IDs remain consumed across token allowance changes and revocation.
    mapping(address user => mapping(bytes32 billId => bool)) private _charged;

    error InvalidPaymentToken(address token);
    error UnsupportedDecimals(uint8 actual);
    error InvalidProvider();
    error InvalidRecipient();
    error NotProvider(address caller);
    error InvalidUser();
    error ZeroCharge();
    error FixedStopAuthority();
    /// @dev Legacy error name preserves the deployed selector; the ID identifies a bill.
    error RequestAlreadyCharged(bytes32 billId);

    event Charged(address indexed user, address indexed provider, bytes32 indexed billId, uint256 amount);

    constructor(address token, address initialOwner, address provider_, address recipient_) Ownable(initialOwner) {
        if (provider_ == address(0)) revert InvalidProvider();
        if (recipient_ == address(0) || recipient_ == address(this)) revert InvalidRecipient();
        if (token == address(0) || token.code.length == 0) revert InvalidPaymentToken(token);
        try IERC20Metadata(token).decimals() returns (uint8 tokenDecimals) {
            if (tokenDecimals != 6) revert UnsupportedDecimals(tokenDecimals);
        } catch {
            revert InvalidPaymentToken(token);
        }
        paymentToken = IERC20(token);
        provider = provider_;
        recipient = recipient_;
    }

    /// @notice The fixed provider pays the fixed recipient once using the user's token allowance.
    /// @dev amount is the bill total for one or more requests. Retries must reuse billId.
    function charge(address user, bytes32 billId, uint256 amount) external nonReentrant whenNotPaused {
        _recordCharge(user, billId, amount);
        paymentToken.safeTransferFrom(user, recipient, amount);

        emit Charged(user, provider, billId, amount);
    }

    /// @dev Shared by both entry points. A failed transfer rolls the paid flag back atomically.
    function _recordCharge(address user, bytes32 billId, uint256 amount) internal {
        if (msg.sender != provider) revert NotProvider(msg.sender);
        if (user == address(0)) revert InvalidUser();
        if (amount == 0) revert ZeroCharge();
        if (_charged[user][billId]) revert RequestAlreadyCharged(billId);
        _charged[user][billId] = true;
    }

    function isCharged(address user, bytes32 billId) external view returns (bool) {
        return _charged[user][billId];
    }

    /// @notice The stop authority is fixed at deployment and never held by the worker.
    function stopAuthority() external view returns (address) {
        return owner();
    }

    function transferOwnership(address) public pure override { revert FixedStopAuthority(); }
    function renounceOwnership() public pure override { revert FixedStopAuthority(); }

    function pause() external onlyOwner {
        _pause();
    }

    function unpause() external onlyOwner {
        _unpause();
    }
}
