// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";

/// @dev Hostile ERC20 behavior for local ReserveDebt tests only.
contract ReserveDebtTokenMock is ERC20 {
    bool public failInbound;
    bool public chargeFee;
    address public failRecipient;
    address public callbackTarget;
    bytes public callbackData;
    bytes public callbackResult;
    bool public callbackSucceeded;
    uint256 public callbackAttempts;

    constructor() ERC20("Reserve debt test token", "DTEST") {}

    function decimals() public pure override returns (uint8) {
        return 6;
    }

    function mint(address user, uint256 amount) external {
        _mint(user, amount);
    }

    function configure(bool fail, bool fee, address recipient) external {
        failInbound = fail;
        chargeFee = fee;
        failRecipient = recipient;
    }

    function setCallback(address target, bytes calldata payload) external {
        callbackTarget = target;
        callbackData = payload;
    }

    function transferFrom(address from, address to, uint256 amount) public override returns (bool) {
        if (failInbound) return false;
        if (callbackTarget != address(0)) {
            ++callbackAttempts;
            (callbackSucceeded, callbackResult) = callbackTarget.call(callbackData);
        }
        if (!chargeFee) return super.transferFrom(from, to, amount);
        _spendAllowance(from, msg.sender, amount);
        _transfer(from, to, amount - 1);
        _burn(from, 1);
        return true;
    }

    function transfer(address to, uint256 amount) public override returns (bool) {
        if (to == failRecipient) return false;
        return super.transfer(to, amount);
    }
}
