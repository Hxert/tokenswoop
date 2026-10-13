// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {TokenSwoopPayG} from "./TokenSwoopPayG.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// @notice Settles a provider-computed bill total in one token transfer.
/// @dev Request details and cross-bill request deduplication are the provider's responsibility.
///      charge and chargeBatch both settle bills with one or more requests, sharing allowance,
///      bill IDs and the reentrancy lock. This entry point retains its deployed ABI and event.
contract TokenSwoopPayGBatch is TokenSwoopPayG {
    using SafeERC20 for IERC20;

    event BatchCharged(address indexed user, address indexed provider, bytes32 indexed billId, uint256 totalAmount);

    constructor(address token, address initialOwner, address provider_, address recipient_)
        TokenSwoopPayG(token, initialOwner, provider_, recipient_)
    {}

    /// @notice Atomically pays a bill once. Failed payments do not consume its ID or token allowance.
    /// @dev billId must remain stable on retries. Its paid state survives token allowance changes.
    function chargeBatch(address user, bytes32 billId, uint256 totalAmount)
        external
        nonReentrant
        whenNotPaused
        returns (uint256)
    {
        _recordCharge(user, billId, totalAmount);
        paymentToken.safeTransferFrom(user, recipient, totalAmount);
        emit BatchCharged(user, provider, billId, totalAmount);
        return totalAmount;
    }
}
