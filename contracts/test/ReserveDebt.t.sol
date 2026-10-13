// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopReserveDebt} from "../src/TokenSwoopReserveDebt.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";
import {TestnetAavePool} from "../src/testnet/TestnetAavePool.sol";
import {CallbackPool} from "./helpers/ReserveMocks.sol";

contract ReserveDebtTest is Test {
    KUSDC token;
    TestnetAavePool pool;
    TokenSwoopReserveDebt reserve;
    address user = address(0x1111);
    address operator = address(0x2222);
    address manager = address(0x3333);
    address treasury = address(0x4444);

    function setUp() public {
        token = new KUSDC(address(this));
        pool = new TestnetAavePool(address(token), address(this));
        reserve = new TokenSwoopReserveDebt(address(token), address(pool), manager, operator, treasury);
        token.mint(user, 1000e6);
        vm.startPrank(user);
        token.approve(address(reserve), 1000e6);
        reserve.deposit(100e6);
        vm.stopPrank();
    }

    function report(bytes32 id, uint256 amount, uint256 epoch) internal {
        bytes32[] memory ids = new bytes32[](1); ids[0] = id;
        uint256[] memory amounts = new uint256[](1); amounts[0] = amount;
        vm.prank(operator); reserve.reportDebts(user,ids,amounts,epoch);
    }

    function testDirectWithdrawalWithoutAnyPlatformInteraction() public {
        vm.prank(user); reserve.withdraw(100e6);
        assertEq(token.balanceOf(user),1000e6);
        assertEq(reserve.totalPrincipal(),0);
    }

    function testOnlyDebtIsFrozenAndRepaymentReleasesIt() public {
        report(bytes32(uint256(1)),7e6,0);
        assertEq(reserve.withdrawableOf(user),93e6);
        vm.prank(user); reserve.withdraw(93e6);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserveDebt.InsufficientWithdrawable.selector,1,0));
        vm.prank(user); reserve.withdraw(1);
        report(bytes32(uint256(1)),0,0);
        vm.prank(user); reserve.withdraw(7e6);
        assertEq(reserve.totalPrincipal(),0);
    }

    function testManagerClearCannotBeUndoneByReplayingOldDebt() public {
        report(bytes32(uint256(1)),10e6,0);
        vm.prank(manager); reserve.clearDebt(user);
        assertEq(reserve.debtEpoch(user),1);
        vm.expectRevert(TokenSwoopReserveDebt.StaleDebtEpoch.selector);
        report(bytes32(uint256(1)),10e6,0);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserveDebt.DebtReleased.selector,bytes32(uint256(1))));
        report(bytes32(uint256(1)),10e6,1);
        report(bytes32(uint256(2)),2e6,1);
        assertEq(reserve.frozenOf(user),2e6);
    }

    function testPauseStopsDepositsOnlyAndRolesCannotImpersonateEachOther() public {
        vm.prank(manager); reserve.setDepositsPaused(true);
        vm.expectRevert(TokenSwoopReserveDebt.DepositsPaused.selector);
        vm.prank(user); reserve.deposit(1);
        vm.prank(user); reserve.withdraw(25e6);
        vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
        vm.prank(operator); reserve.setDepositsPaused(false);
        vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
        vm.prank(operator); reserve.clearDebt(user);
        bytes32[] memory ids=new bytes32[](1);ids[0]=bytes32(uint256(1));
        uint256[] memory amounts=new uint256[](1);amounts[0]=1;
        vm.expectRevert(TokenSwoopReserveDebt.NotOperator.selector);
        vm.prank(manager);reserve.reportDebts(user,ids,amounts,0);
        vm.prank(manager);reserve.setDepositsPaused(false);
        vm.prank(user);reserve.deposit(1e6);
    }

    function testFuzzDebtCannotExposeFrozenPrincipal(uint96 requested) public {
        uint256 amount=bound(uint256(requested),0,200e6);
        report(bytes32(uint256(1)),amount,0);
        uint256 available=amount>=100e6?0:100e6-amount;
        assertEq(reserve.withdrawableOf(user),available);
        if(available>0){vm.prank(user);reserve.withdraw(available);}
        assertEq(reserve.totalPrincipal(),100e6-available);
        assertGe(reserve.totalAssets(),reserve.totalPrincipal());
        vm.prank(manager);reserve.clearDebt(user);
        uint256 remaining=reserve.principalOf(user);
        if(remaining>0){vm.prank(user);reserve.withdraw(remaining);}
        assertEq(token.balanceOf(user),1000e6);
    }

    function testMultipleDebtsCanPartiallySettleWithoutClearingOtherDebt() public {
        report(bytes32(uint256(1)),8e6,0);report(bytes32(uint256(2)),3e6,0);
        report(bytes32(uint256(1)),2e6,0);
        assertEq(reserve.debtOf(user),5e6);
        report(bytes32(uint256(1)),0,0);
        assertEq(reserve.debtOf(user),3e6);
    }

    function testNoRoleCanWithdrawUserPrincipalToItselfOrAsYield() public {
        vm.expectRevert();vm.prank(operator);reserve.withdraw(1);
        vm.expectRevert();vm.prank(manager);reserve.withdraw(1);
        vm.expectRevert(TokenSwoopReserveDebt.ExcessYieldWithdrawal.selector);
        vm.prank(manager);reserve.withdrawYield(1);
        assertEq(reserve.principalOf(user),100e6);
    }

    function testPoolRedemptionFailurePreservesPrincipalAndDebt() public {
        CallbackPool dishonest=new CallbackPool(address(token));
        TokenSwoopReserveDebt r=new TokenSwoopReserveDebt(address(token),address(dishonest),manager,operator,treasury);
        vm.startPrank(user);token.approve(address(r),10e6);r.deposit(10e6);vm.stopPrank();
        dishonest.configure(address(0),"",true,false);
        vm.expectRevert(TokenSwoopReserveDebt.UnexpectedPoolWithdrawal.selector);
        vm.prank(user);r.withdraw(10e6);
        assertEq(r.principalOf(user),10e6);assertEq(r.totalPrincipal(),10e6);
    }

    function testPaymentMultisigStopsBothChargeEntryPointsAndCanResume() public {
        TokenSwoopPayGBatch pay=new TokenSwoopPayGBatch(address(token),manager,operator, operator);
        vm.prank(user);token.approve(address(pay),10e6);
        assertEq(pay.stopAuthority(),manager);
        vm.expectRevert();vm.prank(operator);pay.pause();
        vm.prank(manager);pay.pause();
        vm.expectRevert();vm.prank(operator);pay.charge(user,bytes32(uint256(1)),1e6);
        vm.expectRevert();vm.prank(operator);pay.chargeBatch(user,bytes32(uint256(2)),1e6);
        vm.prank(manager);pay.unpause();
        vm.prank(operator);pay.chargeBatch(user,bytes32(uint256(2)),1e6);
        assertEq(token.balanceOf(operator),1e6);
    }
}
