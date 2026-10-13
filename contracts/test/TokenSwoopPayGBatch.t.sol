// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test, Vm} from "forge-std/Test.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {IERC20Errors} from "@openzeppelin/contracts/interfaces/draft-IERC6093.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";
import {TestPaymentToken} from "./helpers/Mocks.sol";

contract TokenSwoopPayGBatchTest is Test {
    KUSDC internal token;
    TokenSwoopPayGBatch internal payg;
    address internal user;
    address internal provider;
    bytes32 internal constant BILL = keccak256("bill-1");

    function setUp() public {
        vm.warp(1_000_000);
        user = makeAddr("user");
        provider = makeAddr("provider");
        token = new KUSDC(address(this));
        payg = new TokenSwoopPayGBatch(address(token), address(this), provider, provider);
        token.mint(user, 100e6);
        vm.startPrank(user);
        token.approve(address(payg), 10e6);
        vm.stopPrank();
    }

    function _charge(bytes32 billId, uint256 total) internal returns (uint256) {
        vm.prank(provider);
        return payg.chargeBatch(user, billId, total);
    }

    function _state(bytes32 billId) internal view returns (bytes32) {
        return keccak256(
            abi.encode(
                payg.isCharged(user, billId),
                token.balanceOf(user),
                token.balanceOf(provider),
                token.allowance(user, address(payg))
            )
        );
    }

    function testBillTotalUsesOneTransferAndOneBillEvent() public {
        TestPaymentToken countingToken = new TestPaymentToken(6);
        TokenSwoopPayGBatch countingPayg = new TokenSwoopPayGBatch(address(countingToken), address(this), provider, provider);
        countingToken.mint(user, 100e6);
        vm.startPrank(user);
        countingToken.approve(address(countingPayg), 10e6);
        vm.stopPrank();
        vm.recordLogs();
        vm.prank(provider);
        assertEq(countingPayg.chargeBatch(user, BILL, 12600), 12600);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(countingToken.transferFromCalls(), 1);
        assertEq(logs.length, 2);
        assertEq(logs[0].emitter, address(countingToken));
        assertEq(logs[0].topics[0], keccak256("Transfer(address,address,uint256)"));
        assertEq(abi.decode(logs[0].data, (uint256)), 12600);
        assertEq(logs[1].emitter, address(countingPayg));
        assertEq(logs[1].topics[0], keccak256("BatchCharged(address,address,bytes32,uint256)"));
        assertEq(logs[1].topics[1], bytes32(uint256(uint160(user))));
        assertEq(logs[1].topics[2], bytes32(uint256(uint160(provider))));
        assertEq(logs[1].topics[3], BILL);
        uint256 total = abi.decode(logs[1].data, (uint256));
        assertEq(total, 12600);
        assertTrue(countingPayg.isCharged(user, BILL));
        assertEq(countingToken.balanceOf(user), 100e6 - total);
        assertEq(countingToken.balanceOf(provider), total);
        assertEq(countingToken.balanceOf(address(countingPayg)), 0);
        assertEq(countingToken.allowance(user, address(countingPayg)), 10e6 - total);
    }

    function testBillDetailsAreNotRecordedOnChain() public {
        _charge(BILL, 12600);
        assertTrue(payg.isCharged(user, BILL));
        for (uint256 i = 1; i <= 3; ++i) {
            assertFalse(payg.isCharged(user, bytes32(i)));
        }
    }

    function testWholeAllowanceCanBeSettledInOneBill() public {
        _charge(BILL, 10e6);
        assertEq(token.allowance(user, address(payg)), 0);
        assertEq(token.balanceOf(provider), 10e6);
    }

    function testZeroBillTotalRollsBack() public {
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(TokenSwoopPayG.ZeroCharge.selector);
        _charge(BILL, 0);
        assertEq(_state(BILL), beforeState);
    }

    function testSameBillCannotBePaidAgainWithADifferentTotal() public {
        _charge(BILL, 12600);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector, BILL));
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
    }

    function testDistinctBillsShareRemainingTokenAllowance() public {
        _charge(BILL, 6e6);
        bytes32 next = keccak256("bill-2");
        bytes32 beforeState = _state(next);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 4e6, 5e6)
        );
        _charge(next, 5e6);
        assertEq(_state(next), beforeState);
        _charge(next, 4e6);
        assertEq(token.allowance(user, address(payg)), 0);
        assertEq(token.balanceOf(provider), 10e6);
    }

    function testBillPaidThroughChargeCannotReplayThroughChargeBatch() public {
        vm.prank(provider);
        payg.charge(user, BILL, 4200);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector, BILL));
        _charge(BILL, 12600);
        assertEq(_state(BILL), beforeState);
    }

    function testBillPaidThroughChargeBatchCannotReplayThroughCharge() public {
        _charge(BILL, 12600);
        bytes32 beforeState = _state(BILL);
        vm.prank(provider);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector, BILL));
        payg.charge(user, BILL, 4200);
        assertEq(_state(BILL), beforeState);
    }

    function testHugeTotalCannotExceedAllowanceOrConsumeBillId() public {
        vm.prank(user);
        token.approve(address(payg), type(uint256).max);
        _charge(BILL, 4200);
        bytes32 next = keccak256("bill-2");
        bytes32 beforeState = _state(next);
        vm.expectRevert(
            abi.encodeWithSelector(
                IERC20Errors.ERC20InsufficientAllowance.selector,
                address(payg),
                type(uint256).max - 4200,
                type(uint256).max
            )
        );
        _charge(next, type(uint256).max);
        assertEq(_state(next), beforeState);
    }

    function testInsufficientAllowanceRollsBackAndSameBillCanBeRetried() public {
        vm.prank(user);
        token.approve(address(payg), 8400);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 8400, 12600)
        );
        _charge(BILL, 12600);
        assertEq(_state(BILL), beforeState);
        vm.prank(user);
        token.approve(address(payg), 12600);
        _charge(BILL, 12600);
    }

    function testInsufficientBalanceRollsBackAndSameBillCanBeRetried() public {
        vm.prank(user);
        token.transfer(address(this), 100e6 - 8400);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(IERC20Errors.ERC20InsufficientBalance.selector, user, 8400, 12600));
        _charge(BILL, 12600);
        assertEq(_state(BILL), beforeState);
        token.mint(user, 4200);
        _charge(BILL, 12600);
    }

    function testMaximumAllowanceDecrementsByBillTotal() public {
        vm.prank(user);
        token.approve(address(payg), type(uint256).max);
        _charge(BILL, 12600);
        assertEq(token.allowance(user, address(payg)), type(uint256).max - 12600);
    }

    function testReapprovalCannotReplayPaidBill() public {
        _charge(BILL, 12600);
        vm.startPrank(user);
        token.approve(address(payg), 0);
        token.approve(address(payg), 20e6);
        vm.stopPrank();
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector, BILL));
        _charge(BILL, 12600);
        assertEq(_state(BILL), beforeState);
    }

    function testQueuedUnpaidBillCanUseReapprovedAllowance() public {
        vm.prank(user);
        token.approve(address(payg), 0);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 12600)
        );
        _charge(BILL, 12600);
        assertFalse(payg.isCharged(user, BILL));
        vm.prank(user);
        token.approve(address(payg), 12600);
        _charge(BILL, 12600);
        assertTrue(payg.isCharged(user, BILL));
        assertEq(token.allowance(user, address(payg)), 0);
    }

    function testUnauthorizedCallerCannotChargeForProvider() public {
        vm.prank(makeAddr("other-provider"));
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.NotProvider.selector, makeAddr("other-provider")));
        payg.chargeBatch(user, BILL, 12600);
    }

    function testZeroUserRejected() public {
        vm.prank(provider);
        vm.expectRevert(TokenSwoopPayG.InvalidUser.selector);
        payg.chargeBatch(address(0), BILL, 12600);
    }

    function testApproveZeroRevokesBillPayment() public {
        vm.prank(user);
        token.approve(address(payg), 0);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 12600)
        );
        _charge(BILL, 12600);
        assertEq(_state(BILL), beforeState);
    }

    function testTokenAllowanceDoesNotExpireInPaymentContract() public {
        vm.warp(block.timestamp + 365 days);
        _charge(BILL, 12600);
        assertTrue(payg.isCharged(user, BILL));
    }

    function testPauseBlocksBillsButUserCanRevokeTokenAllowance() public {
        payg.pause();
        vm.expectRevert(Pausable.EnforcedPause.selector);
        _charge(BILL, 12600);
        vm.prank(user);
        token.approve(address(payg), 0);
        payg.unpause();
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 12600)
        );
        _charge(BILL, 12600);
    }

    function testTokenPauseRollsBackBill() public {
        token.pause();
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        _charge(BILL, 12600);
        assertEq(_state(BILL), beforeState);
    }

    function testBlacklistRollsBackBill() public {
        token.blacklist(provider);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, provider));
        _charge(BILL, 12600);
        assertEq(_state(BILL), beforeState);
    }

    function testBatchConstructorRejectsZeroProvider() public {
        vm.expectRevert(TokenSwoopPayG.InvalidProvider.selector);
        new TokenSwoopPayGBatch(address(token), address(this), address(0), address(0));
    }

    function testOnlyFixedProviderCanSettleBills() public {
        bytes32 beforeState = _state(BILL);
        address[3] memory callers = [makeAddr("other-provider"), user, address(this)];
        for (uint256 i; i < callers.length; ++i) {
            vm.prank(callers[i]);
            vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.NotProvider.selector, callers[i]));
            payg.chargeBatch(user, BILL, 12600);
            assertEq(_state(BILL), beforeState);
        }
        _charge(BILL, 12600);
        assertEq(token.balanceOf(provider), 12600);
    }

    function testBillIdsAreScopedToUser() public {
        address other = makeAddr("other-user");
        token.mint(other, 1e6);
        vm.startPrank(other);
        token.approve(address(payg), 1e6);
        vm.stopPrank();
        _charge(BILL, 12600);
        vm.prank(provider);
        payg.chargeBatch(other, BILL, 12600);
        assertTrue(payg.isCharged(user, BILL));
        assertTrue(payg.isCharged(other, BILL));
        assertEq(token.balanceOf(provider), 25200);
    }

    function testTokenFalseReturnRollsBackBillAndTransferCounter() public {
        TestPaymentToken badToken = new TestPaymentToken(6);
        TokenSwoopPayGBatch badPayg = new TokenSwoopPayGBatch(address(badToken), address(this), provider, provider);
        badToken.mint(user, 100e6);
        badToken.configure(true, TokenSwoopPayG(address(0)));
        vm.startPrank(user);
        badToken.approve(address(badPayg), 10e6);
        vm.stopPrank();
        vm.prank(provider);
        vm.expectRevert(abi.encodeWithSelector(SafeERC20.SafeERC20FailedOperation.selector, address(badToken)));
        badPayg.chargeBatch(user, BILL, 12600);
        assertEq(badToken.allowance(user, address(badPayg)), 10e6);
        assertFalse(badPayg.isCharged(user, BILL));
        assertEq(badToken.balanceOf(user), 100e6);
        assertEq(badToken.transferFromCalls(), 0);
    }

    function _reentry(bool outerBatch, bool innerBatch) internal {
        TestPaymentToken malicious = new TestPaymentToken(6);
        TokenSwoopPayGBatch guarded = new TokenSwoopPayGBatch(address(malicious), address(this), address(malicious), address(malicious));
        malicious.mint(user, 100e6);
        if (innerBatch) malicious.configureBatchReentry(guarded);
        else malicious.configure(false, guarded);
        vm.startPrank(user);
        malicious.approve(address(guarded), 10e6);
        vm.stopPrank();
        vm.prank(address(malicious));
        if (outerBatch) guarded.chargeBatch(user, BILL, 12600);
        else guarded.charge(user, BILL, 4200);
        assertTrue(malicious.attemptedReentry());
        assertFalse(malicious.reentrySucceeded());
        assertEq(malicious.allowance(user, address(guarded)), 10e6 - (outerBatch ? 12600 : 4200));
        assertEq(malicious.balanceOf(address(malicious)), outerBatch ? 12600 : 4200);
        assertFalse(guarded.isCharged(user, keccak256("reentrant")));
        assertFalse(guarded.isCharged(user, keccak256("reentrant-batch")));
    }

    function testBatchToSingleReentryBlocked() public {
        _reentry(true, false);
    }

    function testSingleToBatchReentryBlocked() public {
        _reentry(false, true);
    }

    function testBatchToBatchReentryBlocked() public {
        _reentry(true, true);
    }

    function testSingleAndBatchShareOneTokenAllowance() public {
        vm.prank(provider);
        payg.charge(user, keccak256("single-request"), 6e6);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 4e6, 5e6)
        );
        _charge(BILL, 5e6);
        assertEq(_state(BILL), beforeState);
        _charge(BILL, 4e6);
        assertEq(token.allowance(user, address(payg)), 0);
    }

    function testLegacyVersionedBillInterfaceRejected() public {
        bytes32 beforeState = _state(BILL);
        vm.prank(provider);
        (bool success,) = address(payg)
            .call(abi.encodeWithSignature("chargeBatch(address,uint256,bytes32,uint256)", user, 1, BILL, 12600));
        assertFalse(success);
        assertEq(_state(BILL), beforeState);
    }

    function testOldArrayInterfaceRejected() public {
        bytes32[] memory ids = new bytes32[](1);
        ids[0] = BILL;
        uint256[] memory amounts = new uint256[](1);
        amounts[0] = 12600;
        bytes32 beforeState = _state(BILL);
        vm.prank(provider);
        (bool success,) = address(payg)
            .call(abi.encodeWithSignature("chargeBatch(address,uint256,bytes32[],uint256[])", user, 1, ids, amounts));
        assertFalse(success);
        assertEq(_state(BILL), beforeState);
    }

    function testFuzzTwoBillTotalsConserveFundsAndStayWithinAllowance(uint64 first, uint64 second) public {
        uint256 firstTotal = bound(uint256(first), 1, 5e6);
        uint256 secondTotal = bound(uint256(second), 1, 5e6);
        _charge(BILL, firstTotal);
        _charge(keccak256("bill-2"), secondTotal);
        uint256 total = firstTotal + secondTotal;
        assertEq(token.balanceOf(user) + token.balanceOf(provider), 100e6);
        assertEq(token.allowance(user, address(payg)), 10e6 - total);
    }

    function testFuzzBillIdCannotBePaidTwice(bytes32 billId, uint96 rawTotal) public {
        uint256 total = bound(uint256(rawTotal), 1, 10e6);
        _charge(billId, total);
        bytes32 beforeState = _state(billId);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector, billId));
        _charge(billId, 1);
        assertEq(_state(billId), beforeState);
    }
}
