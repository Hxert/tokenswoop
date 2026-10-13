// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopReserve} from "../src/TokenSwoopReserve.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {CallbackPool} from "./helpers/ReserveMocks.sol";
import {TestPaymentToken} from "./helpers/Mocks.sol";

contract ReserveSecurityTest is Test {
    KUSDC internal token;
    CallbackPool internal pool;
    TokenSwoopReserve internal reserve;
    address internal user = address(0x1111);
    address internal other = address(0x2222);

    function setUp() public {
        token = new KUSDC(address(this));
        pool = new CallbackPool(address(token));
        reserve = new TokenSwoopReserve(address(token), address(pool), address(this), address(pool), address(0x3333));
        token.mint(address(this), 1e6);
        token.approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
        token.mint(user, 100e6);
        token.mint(other, 100e6);
    }

    function _deposit(address account, uint256 amount) internal {
        vm.startPrank(account);
        token.approve(address(reserve), amount);
        reserve.deposit(amount);
        vm.stopPrank();
    }

    function testPoolCallbackCannotApproveWithdrawalDuringDeposit() public {
        _deposit(user, 50e6);
        vm.prank(user);
        uint256 id = reserve.requestWithdrawal(50e6);
        pool.configure(
            address(reserve),
            abi.encodeCall(reserve.approveWithdrawal, (user, id, uint64(block.timestamp + 1 hours))),
            false,
            false
        );
        _deposit(other, 10e6);
        assertFalse(pool.callbackSucceeded());
        assertEq(reserve.getWithdrawal(user).approvedUntil, 0);
        assertEq(reserve.totalPrincipal(), 60e6);
    }

    function testPoolCallbackCannotClearOtherUsersApprovalDuringWithdrawal() public {
        _deposit(user, 50e6);
        _deposit(other, 50e6);
        vm.prank(user);
        uint256 id = reserve.requestWithdrawal(50e6);
        vm.prank(other);
        uint256 otherId = reserve.requestWithdrawal(50e6);
        vm.startPrank(address(pool));
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 hours));
        reserve.approveWithdrawal(other, otherId, uint64(block.timestamp + 1 hours));
        vm.stopPrank();
        uint64 otherExpiry = reserve.getWithdrawal(other).approvedUntil;
        pool.configure(
            address(reserve),
            abi.encodeCall(reserve.clearWithdrawalApproval, (other, otherId, bytes32(0))),
            false,
            false
        );
        vm.prank(user);
        reserve.withdraw(id);
        assertFalse(pool.callbackSucceeded());
        assertEq(reserve.getWithdrawal(other).approvedUntil, otherExpiry);
        assertEq(reserve.principalOf(other), 50e6);
    }

    function testIncorrectPoolReturnRevertsEveryWithdrawalWriteAndTransfer() public {
        _deposit(user, 50e6);
        vm.prank(user);
        uint256 id = reserve.requestWithdrawal(50e6);
        vm.prank(address(pool));
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 hours));
        pool.configure(address(0), "", true, false);
        vm.prank(user);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.UnexpectedPoolWithdrawal.selector, 49e6, 49e6 - 1));
        reserve.withdraw(id);
        assertEq(token.balanceOf(user), 50e6);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(reserve.aToken().balanceOf(address(reserve)), 50e6);
        assertTrue(reserve.isWithdrawalPending(user));
    }

    function testPoolTakingFundsWithoutMintingReceiptCannotCreatePrincipal() public {
        pool.configure(address(0), "", false, true);
        vm.startPrank(user);
        token.approve(address(reserve), 50e6);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.InsufficientBacking.selector, 1e6, 50e6));
        reserve.deposit(50e6);
        vm.stopPrank();
        assertEq(token.balanceOf(user), 100e6);
        assertEq(reserve.totalPrincipal(), 0);
        assertEq(token.allowance(user, address(reserve)), 50e6);
    }

    function testFalseReturningTokenCannotCreatePrincipal() public {
        TestPaymentToken falseToken = new TestPaymentToken(6);
        CallbackPool falsePool = new CallbackPool(address(falseToken));
        TokenSwoopReserve vault =
            new TokenSwoopReserve(address(falseToken), address(falsePool), address(this), user, other);
        falseToken.mint(user, 50e6);
        vm.prank(user);
        falseToken.approve(address(vault), 50e6);
        falseToken.configure(true, TokenSwoopPayG(address(0)));
        vm.prank(user);
        vm.expectRevert(abi.encodeWithSelector(SafeERC20.SafeERC20FailedOperation.selector, address(falseToken)));
        vault.deposit(50e6);
        assertEq(vault.totalPrincipal(), 0);
        assertEq(falseToken.balanceOf(user), 50e6);
    }
}
