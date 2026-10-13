// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test, Vm} from "forge-std/Test.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopReserve} from "../src/TokenSwoopReserve.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TestnetAavePool} from "../src/testnet/TestnetAavePool.sol";
import {TestPaymentToken} from "./helpers/Mocks.sol";

contract TokenSwoopReserveTest is Test {
    KUSDC internal token;
    TestnetAavePool internal pool;
    TokenSwoopReserve internal reserve;
    address internal user = address(0xA11CE);
    address internal secondUser = address(0xB0B);
    address internal operator = address(0xFEED);
    address internal treasury = address(0xCAFE);

    function setUp() public {
        token = new KUSDC(address(this));
        pool = new TestnetAavePool(address(token), address(this));
        reserve = new TokenSwoopReserve(address(token), address(pool), address(this), operator, treasury);
        token.mint(user, 2000e6);
        token.mint(secondUser, 2000e6);
        token.mint(address(this), 100e6);
        token.approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
    }

    function _deposit(address account, uint256 amount) internal {
        vm.startPrank(account);
        token.approve(address(reserve), amount);
        reserve.deposit(amount);
        vm.stopPrank();
    }

    function _request(address account, uint256 amount) internal returns (uint256 id) {
        vm.prank(account);
        return reserve.requestWithdrawal(amount);
    }

    function _approve(address account, uint256 id) internal {
        vm.prank(operator);
        reserve.approveWithdrawal(account, id, uint64(block.timestamp + 1 hours));
    }

    function _withdraw(address account, uint256 id) internal {
        vm.prank(account);
        reserve.withdraw(id);
    }

    function _yield(uint256 amount) internal {
        token.approve(address(pool), amount);
        pool.accrueYield(amount);
    }

    function testDepositSuppliesAaveAndCreditsOnlyCallerPrincipal() public {
        vm.recordLogs();
        _deposit(user, 50e6);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        bool found;
        for (uint256 i; i < logs.length; ++i) {
            if (
                logs[i].emitter == address(reserve)
                    && logs[i].topics[0] == keccak256("Deposited(address,uint256,uint256)")
            ) {
                found = true;
                assertEq(address(uint160(uint256(logs[i].topics[1]))), user);
                (uint256 amount, uint256 principal) = abi.decode(logs[i].data, (uint256, uint256));
                assertEq(amount, 50e6);
                assertEq(principal, 50e6);
            }
        }
        assertTrue(found);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(reserve.totalPrincipal(), 50e6);
        assertEq(reserve.aToken().balanceOf(address(reserve)), 50e6);
        assertEq(token.balanceOf(address(reserve)), 1e6);
        assertEq(token.balanceOf(user), 1950e6);
        assertEq(token.allowance(user, address(reserve)), 0);
        assertEq(token.allowance(address(reserve), address(pool)), 0);
    }

    function testBufferDoesNotCreatePrincipal() public view {
        assertEq(reserve.totalPrincipal(), 0);
        assertEq(reserve.principalOf(address(this)), 0);
        assertEq(reserve.totalAssets(), 1e6);
        assertEq(reserve.availableYield(), 0);
    }

    function testCumulativeDepositsRecordExactPrincipalWithoutMinimumThreshold() public {
        _deposit(user, 1);
        assertEq(reserve.principalOf(user), 1);
        _deposit(user, 17e6 - 1);
        assertEq(reserve.principalOf(user), 17e6);
        _deposit(user, 123e6);
        assertEq(reserve.principalOf(user), 140e6);
        assertEq(reserve.totalPrincipal(), 140e6);
    }

    function testAccruedYieldDoesNotIncreaseUserPrincipal() public {
        _deposit(user, 49e6);
        _yield(10e6);
        assertEq(reserve.principalOf(user), 49e6);
        assertEq(reserve.totalPrincipal(), 49e6);
        assertGt(reserve.aToken().balanceOf(address(reserve)), 50e6);
    }

    function testMultipleUsersHaveIndependentPrincipalAndWithdrawals() public {
        _deposit(user, 50e6);
        _deposit(secondUser, 200e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        _withdraw(user, id);
        assertEq(reserve.principalOf(user), 0);
        assertEq(reserve.principalOf(secondUser), 200e6);
        assertEq(reserve.totalPrincipal(), 200e6);
        assertFalse(reserve.isWithdrawalPending(user));
    }

    function testAdministrationIsOwnerOnlyAndCannotWithdrawUserPrincipal() public {
        _deposit(user, 50e6);
        vm.startPrank(user);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user));
        reserve.setSettlementOperator(user);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user));
        reserve.withdrawYield(1);
        vm.stopPrank();
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.ExcessYieldWithdrawal.selector, 50e6, 0));
        reserve.withdrawYield(50e6);
        assertEq(token.balanceOf(treasury), 0);
        assertEq(reserve.totalPrincipal(), 50e6);
    }

    function testConstructorRequiresSixDecimalTokenAndMatchingPool() public {
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.InvalidToken.selector, address(0)));
        new TokenSwoopReserve(address(0), address(pool), address(this), operator, treasury);
        TestPaymentToken wrongDecimals = new TestPaymentToken(18);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.UnsupportedDecimals.selector, uint8(18)));
        new TokenSwoopReserve(address(wrongDecimals), address(pool), address(this), operator, treasury);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.InvalidPool.selector, address(0)));
        new TokenSwoopReserve(address(token), address(0), address(this), operator, treasury);
        KUSDC anotherToken = new KUSDC(address(this));
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.InvalidAToken.selector, address(0)));
        new TokenSwoopReserve(address(anotherToken), address(pool), address(this), operator, treasury);
        vm.expectRevert(TokenSwoopReserve.InvalidAddress.selector);
        new TokenSwoopReserve(address(token), address(pool), address(this), address(0), treasury);
    }

    function testZeroDepositsAndBufferFundingRejected() public {
        vm.expectRevert(TokenSwoopReserve.ZeroAmount.selector);
        reserve.deposit(0);
        vm.expectRevert(TokenSwoopReserve.ZeroAmount.selector);
        reserve.fundBuffer(0);
    }

    function testDepositFailureDoesNotConsumePrincipalOrAllowance() public {
        vm.startPrank(user);
        token.approve(address(reserve), 10e6);
        vm.expectRevert();
        reserve.deposit(50e6);
        vm.stopPrank();
        assertEq(reserve.totalPrincipal(), 0);
        assertEq(token.allowance(user, address(reserve)), 10e6);
        assertEq(token.balanceOf(user), 2000e6);
        assertEq(reserve.aToken().balanceOf(address(reserve)), 0);
    }

    function testInsufficientBalanceDepositRollsBack() public {
        vm.startPrank(user);
        token.approve(address(reserve), 2001e6);
        vm.expectRevert();
        reserve.deposit(2001e6);
        vm.stopPrank();
        assertEq(reserve.principalOf(user), 0);
    }

    function testPoolPausedDepositRollsBackBothTransfersAndApprovals() public {
        pool.pause();
        vm.startPrank(user);
        token.approve(address(reserve), 50e6);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        reserve.deposit(50e6);
        vm.stopPrank();
        assertEq(reserve.totalPrincipal(), 0);
        assertEq(token.balanceOf(user), 2000e6);
        assertEq(token.allowance(user, address(reserve)), 50e6);
        assertEq(token.allowance(address(reserve), address(pool)), 0);
    }

    function testTokenPauseAndBlacklistBlockDepositAtomically() public {
        vm.prank(user);
        token.approve(address(reserve), 50e6);
        token.pause();
        vm.prank(user);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        reserve.deposit(50e6);
        token.unpause();
        token.blacklist(address(pool));
        vm.prank(user);
        vm.expectRevert();
        reserve.deposit(50e6);
        assertEq(reserve.totalPrincipal(), 0);
        assertEq(token.allowance(user, address(reserve)), 50e6);
    }

    function testTinyAaveSupplyAtHigherIndexRevertsWithoutCreditingPrincipal() public {
        _deposit(secondUser, 100e6);
        _yield(10e6);
        vm.prank(user);
        token.approve(address(reserve), 1);
        vm.prank(user);
        vm.expectRevert(TestnetAavePool.InvalidAmount.selector);
        reserve.deposit(1);
        assertEq(reserve.principalOf(user), 0);
        assertEq(reserve.totalPrincipal(), 100e6);
        assertEq(token.allowance(user, address(reserve)), 1);
    }

    function testUnfundedRoundingShortfallCannotOvercreditNewPrincipal() public {
        _deposit(secondUser, 3e6);
        _yield(1e6);
        TokenSwoopReserve unfunded =
            new TokenSwoopReserve(address(token), address(pool), address(this), operator, treasury);
        vm.startPrank(user);
        token.approve(address(unfunded), 50e6);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.InsufficientBacking.selector, 50e6 - 1, 50e6));
        unfunded.deposit(50e6);
        vm.stopPrank();
        assertEq(unfunded.totalPrincipal(), 0);
        token.approve(address(unfunded), 1e6);
        unfunded.fundBuffer(1e6);
        vm.prank(user);
        unfunded.deposit(50e6);
        assertEq(unfunded.principalOf(user), 50e6);
        assertGe(unfunded.totalAssets(), 50e6);
    }

    function testRequestRequiresPositiveOwnedPrincipal() public {
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.ZeroAmount.selector);
        reserve.requestWithdrawal(0);
        vm.prank(user);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.InsufficientPrincipal.selector, 1, 0));
        reserve.requestWithdrawal(1);
    }

    function testActiveWithdrawalPreventsNewDepositOrReplacement() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 10e6);
        assertEq(id, 1);
        assertTrue(reserve.isWithdrawalPending(user));
        vm.startPrank(user);
        vm.expectRevert(TokenSwoopReserve.WithdrawalAlreadyPending.selector);
        reserve.deposit(1);
        vm.expectRevert(TokenSwoopReserve.WithdrawalAlreadyPending.selector);
        reserve.requestWithdrawal(5e6);
        vm.stopPrank();
    }

    function testNoWithdrawalBeforeOperatorFeeClearanceApproval() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.WithdrawalNotApproved.selector);
        reserve.withdraw(id);
        assertEq(reserve.principalOf(user), 50e6);
        assertTrue(reserve.isWithdrawalPending(user));
    }

    function testOnlyGatewayCanApproveEvenOwnerCannotApproveByDefault() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        vm.expectRevert(TokenSwoopReserve.NotSettlementOperator.selector);
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 hours));
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.NotSettlementOperator.selector);
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 hours));
    }

    function testApprovalMustExpireInFuture() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        vm.prank(operator);
        vm.expectRevert(TokenSwoopReserve.InvalidApprovalExpiry.selector);
        reserve.approveWithdrawal(user, id, uint64(block.timestamp));
    }

    function testExpiryKeepsApiFrozenAndRequestCanBeReapproved() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        uint64 expiry = reserve.getWithdrawal(user).approvedUntil;
        vm.warp(expiry);
        vm.prank(user);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.ApprovalExpired.selector, expiry));
        reserve.withdraw(id);
        assertTrue(reserve.isWithdrawalPending(user));
        _approve(user, id);
        _withdraw(user, id);
        assertEq(reserve.principalOf(user), 0);
    }

    function testCancellationInvalidatesApprovalAndNewRequestHasNewId() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        vm.prank(user);
        reserve.cancelWithdrawal();
        assertFalse(reserve.isWithdrawalPending(user));
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.WithdrawalNotPending.selector);
        reserve.withdraw(id);
        uint256 newId = _request(user, 10e6);
        assertEq(newId, id + 1);
        vm.prank(operator);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserve.WithdrawalVersionMismatch.selector, newId, id));
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 hours));
        _approve(user, newId);
        _withdraw(user, newId);
        assertEq(reserve.principalOf(user), 40e6);
    }

    function testGatewayCanClearApprovalIfFeeClearanceWasIncorrect() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        vm.prank(operator);
        reserve.clearWithdrawalApproval(user, id, keccak256("unsettled-bill"));
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.WithdrawalNotApproved.selector);
        reserve.withdraw(id);
        assertTrue(reserve.isWithdrawalPending(user));
    }

    function testChangingOperatorInvalidatesAllApprovals() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        address next = address(0x1234);
        reserve.setSettlementOperator(next);
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.StaleOperatorApproval.selector);
        reserve.withdraw(id);
        vm.prank(operator);
        vm.expectRevert(TokenSwoopReserve.NotSettlementOperator.selector);
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 hours));
        vm.prank(next);
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 hours));
        _withdraw(user, id);
    }

    function testReusingOperatorAddressStillInvalidatesApproval() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        reserve.setSettlementOperator(operator);
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.StaleOperatorApproval.selector);
        reserve.withdraw(id);
    }

    function testUserWithdrawalCannotBeCalledByGatewayOrRedirected() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        vm.prank(operator);
        vm.expectRevert(TokenSwoopReserve.WithdrawalNotPending.selector);
        reserve.withdraw(id);
        _withdraw(user, id);
        assertEq(token.balanceOf(user), 2000e6);
        assertEq(token.balanceOf(operator), 0);
        assertEq(token.balanceOf(treasury), 0);
    }

    function testPartialWithdrawalUpdatesPrincipalAndHasAccurateReceipt() public {
        _deposit(user, 200e6);
        uint256 id = _request(user, 151e6);
        _approve(user, id);
        vm.recordLogs();
        _withdraw(user, id);
        assertEq(reserve.principalOf(user), 49e6);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        bool found;
        for (uint256 i; i < logs.length; ++i) {
            if (
                logs[i].emitter == address(reserve)
                    && logs[i].topics[0] == keccak256("Withdrawn(address,uint256,uint256,uint256)")
            ) {
                found = true;
                assertEq(address(uint160(uint256(logs[i].topics[1]))), user);
                assertEq(uint256(logs[i].topics[2]), id);
                (uint256 amount, uint256 principal) = abi.decode(logs[i].data, (uint256, uint256));
                assertEq(amount, 151e6);
                assertEq(principal, 49e6);
            }
        }
        assertTrue(found);
    }

    function testWithdrawIsOnceOnly() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        _withdraw(user, id);
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserve.WithdrawalNotPending.selector);
        reserve.withdraw(id);
        assertEq(reserve.totalPrincipal(), 0);
    }

    function testIlliquidWithdrawalRollsBackAndSameRequestCanRetry() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        pool.setWithdrawalLimit(0);
        TokenSwoopReserve.WithdrawalRequest memory beforeRequest = reserve.getWithdrawal(user);
        uint256 assets = reserve.totalAssets();
        vm.prank(user);
        vm.expectRevert(TestnetAavePool.InsufficientLiquidity.selector);
        reserve.withdraw(id);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(reserve.totalAssets(), assets);
        assertEq(keccak256(abi.encode(reserve.getWithdrawal(user))), keccak256(abi.encode(beforeRequest)));
        assertEq(token.balanceOf(user), 1950e6);
        pool.setWithdrawalLimit(type(uint256).max);
        _withdraw(user, id);
        assertEq(reserve.principalOf(user), 0);
    }

    function testTokenPauseWithdrawalRollsBackButCancelRemainsAvailable() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        token.pause();
        vm.prank(user);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        reserve.withdraw(id);
        assertEq(reserve.principalOf(user), 50e6);
        vm.prank(user);
        reserve.cancelWithdrawal();
        assertFalse(reserve.isWithdrawalPending(user));
    }

    function testBlacklistedUserWithdrawalRollsBackThenCanRetry() public {
        _deposit(user, 50e6);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        token.blacklist(user);
        vm.prank(user);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, user));
        reserve.withdraw(id);
        assertEq(reserve.principalOf(user), 50e6);
        assertTrue(reserve.isWithdrawalPending(user));
        token.unBlacklist(user);
        _withdraw(user, id);
        assertEq(token.balanceOf(user), 2000e6);
    }

    function testReservePauseAllowsExitManagementButBlocksNewDepositsAndYield() public {
        _deposit(user, 50e6);
        _yield(10e6);
        reserve.pause();
        vm.prank(secondUser);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        reserve.deposit(50e6);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        reserve.withdrawYield(1);
        uint256 id = _request(user, 50e6);
        _approve(user, id);
        _withdraw(user, id);
        assertEq(reserve.principalOf(user), 0);
        reserve.unpause();
    }

    function testYieldCanOnlyGoToFixedTreasuryAndProtectsAllPrincipal() public {
        _deposit(user, 1000e6);
        _yield(10e6);
        assertEq(reserve.availableYield(), 10e6);
        reserve.withdrawYield(10e6);
        assertEq(token.balanceOf(treasury), 10e6);
        assertEq(reserve.principalOf(user), 1000e6);
        assertGe(reserve.totalAssets(), reserve.totalPrincipal());
        assertEq(reserve.availableYield(), 0);
        uint256 id = _request(user, 1000e6);
        _approve(user, id);
        _withdraw(user, id);
        assertEq(token.balanceOf(user), 2000e6);
        assertEq(reserve.totalPrincipal(), 0);
    }

    function testYieldWithoutLiquidityDoesNotBurnPrincipalOrReceiptTokens() public {
        _deposit(user, 50e6);
        _yield(10e6);
        pool.setWithdrawalLimit(0);
        uint256 assets = reserve.totalAssets();
        vm.expectRevert(TestnetAavePool.InsufficientLiquidity.selector);
        reserve.withdrawYield(10e6);
        assertEq(reserve.totalAssets(), assets);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(token.balanceOf(treasury), 0);
    }

    function testYieldRoundingBufferIsKeptAndZeroHarvestRejected() public {
        vm.expectRevert(TokenSwoopReserve.ZeroAmount.selector);
        reserve.withdrawYield(0);
        _deposit(user, 50e6);
        _yield(500_000);
        uint256 available = reserve.availableYield();
        assertLe(available, 500_000);
        vm.expectRevert(
            abi.encodeWithSelector(TokenSwoopReserve.ExcessYieldWithdrawal.selector, available + 1, available)
        );
        reserve.withdrawYield(available + 1);
    }

    function testPayGStillPaysFromWalletAndCannotConsumeReserve() public {
        _deposit(user, 50e6);
        TokenSwoopPayGBatch batch = new TokenSwoopPayGBatch(address(token), address(this), operator, operator);
        vm.startPrank(user);
        token.approve(address(batch), 10e6);
        vm.stopPrank();
        vm.prank(operator);
        batch.chargeBatch(user, keccak256("reserve-independent-payg"), 12600);
        assertEq(token.balanceOf(operator), 12600);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(reserve.aToken().balanceOf(address(reserve)), 50e6);
        vm.prank(user);
        token.approve(address(batch), 0);
        vm.prank(operator);
        vm.expectRevert();
        batch.chargeBatch(user, keccak256("cannot-use-reserve"), 12600);
        assertEq(reserve.totalPrincipal(), 50e6);
    }

    function testContractWalletCanDepositAndRequestWithdrawal() public {
        // Token/PayG signature compatibility has separate ERC-1271 coverage. Reserve uses msg.sender.
        address wallet = address(new TestPaymentToken(6));
        token.mint(wallet, 50e6);
        _deposit(wallet, 50e6);
        uint256 id = _request(wallet, 50e6);
        _approve(wallet, id);
        _withdraw(wallet, id);
        assertEq(token.balanceOf(wallet), 50e6);
    }

    function testFuzzPrincipalConservationAcrossDepositAndPartialWithdrawal(uint256 rawDeposit, uint256 rawWithdraw)
        public
    {
        uint256 deposited = bound(rawDeposit, 2, 2000e6);
        uint256 withdrawn = bound(rawWithdraw, 1, deposited);
        _deposit(user, deposited);
        uint256 id = _request(user, withdrawn);
        _approve(user, id);
        _withdraw(user, id);
        assertEq(reserve.principalOf(user), deposited - withdrawn);
        assertEq(reserve.totalPrincipal(), deposited - withdrawn);
        assertEq(token.balanceOf(user), 2000e6 - deposited + withdrawn);
        assertGe(reserve.totalAssets(), reserve.totalPrincipal());
    }

    function testFuzzYieldHarvestNeverChangesUserPrincipal(uint256 rawDeposit, uint256 rawYield) public {
        uint256 deposited = bound(rawDeposit, 50e6, 2000e6);
        uint256 fundedYield = bound(rawYield, 2, 99e6);
        _deposit(user, deposited);
        _yield(fundedYield);
        uint256 available = reserve.availableYield();
        if (available != 0) reserve.withdrawYield(available);
        assertEq(reserve.principalOf(user), deposited);
        assertGe(reserve.totalAssets(), deposited);
        uint256 id = _request(user, deposited);
        _approve(user, id);
        _withdraw(user, id);
        assertEq(token.balanceOf(user), 2000e6);
        assertEq(reserve.totalPrincipal(), 0);
    }
}
