// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {IAaveV3Pool} from "../../src/interfaces/IAaveV3Pool.sol";

contract CallbackReceipt is ERC20 {
    using SafeERC20 for IERC20;
    address public immutable POOL;
    address public immutable UNDERLYING_ASSET_ADDRESS;

    constructor(address pool, address token) ERC20("Callback receipt", "CALLBACK") {
        POOL = pool;
        UNDERLYING_ASSET_ADDRESS = token;
    }

    function decimals() public pure override returns (uint8) {
        return 6;
    }

    function mint(address account, uint256 amount) external {
        require(msg.sender == POOL);
        _mint(account, amount);
    }

    function redeem(address account, uint256 amount, address to) external {
        require(msg.sender == POOL);
        _burn(account, amount);
        IERC20(UNDERLYING_ASSET_ADDRESS).safeTransfer(to, amount);
    }
}

/// @dev Deliberately dishonest pool for safety tests only; never included in deployment scripts.
contract CallbackPool is IAaveV3Pool {
    using SafeERC20 for IERC20;
    IERC20 public immutable token;
    CallbackReceipt public immutable receipt;
    address public target;
    bytes public callback;
    bool public callbackSucceeded;
    bytes public callbackResult;
    uint256 public callbackAttempts;
    bool public shortReturn;
    bool public skipMint;

    constructor(address underlying) {
        token = IERC20(underlying);
        receipt = new CallbackReceipt(address(this), underlying);
    }

    function configure(address target_, bytes memory callback_, bool shortReturn_, bool skipMint_) external {
        target = target_;
        callback = callback_;
        shortReturn = shortReturn_;
        skipMint = skipMint_;
    }

    function getReserveAToken(address) external view returns (address) {
        return address(receipt);
    }

    function supply(address, uint256 amount, address onBehalfOf, uint16) external {
        token.safeTransferFrom(msg.sender, address(receipt), amount);
        if (!skipMint) receipt.mint(onBehalfOf, amount);
        _callback();
    }

    function withdraw(address, uint256 amount, address to) external returns (uint256) {
        receipt.redeem(msg.sender, amount, to);
        _callback();
        return shortReturn ? amount - 1 : amount;
    }

    function _callback() internal {
        if (target != address(0)) {
            ++callbackAttempts;
            (callbackSucceeded, callbackResult) = target.call(callback);
        }
    }
}
