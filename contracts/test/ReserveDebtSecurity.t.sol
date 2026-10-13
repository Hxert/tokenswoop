// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {TokenSwoopReserveDebt} from "../src/TokenSwoopReserveDebt.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {CallbackPool} from "./helpers/ReserveMocks.sol";
import {ReserveDebtTokenMock} from "./helpers/ReserveDebtMocks.sol";
import {TestPaymentToken} from "./helpers/Mocks.sol";

contract ReserveDebtSecurityTest is Test {
    KUSDC internal token;
    CallbackPool internal pool;
    TokenSwoopReserveDebt internal reserve;
    address internal constant USER = address(0x1111);
    address internal constant OTHER = address(0x2222);
    address internal constant MANAGER = address(0x3333);
    address internal constant TREASURY = address(0x4444);
    address internal constant STRANGER = address(0x5555);

    function setUp() public {
        token = new KUSDC(address(this));
        pool = new CallbackPool(address(token));
        // The callback pool is an authorized operator, so callback rejection
        // must come from the reentrancy guard, not merely from access control.
        reserve = new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, address(pool), TREASURY);
        token.mint(USER, 200e6);
        token.mint(OTHER, 200e6);
        _deposit(reserve, USER, 100e6);
        _deposit(reserve, OTHER, 70e6);
        token.mint(address(this), 1e6);
        token.approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
    }

    function _deposit(TokenSwoopReserveDebt vault, address user, uint256 amount) internal {
        vm.startPrank(user);
        vault.reserveToken().approve(address(vault), amount);
        vault.deposit(amount);
        vm.stopPrank();
    }

    function _report(TokenSwoopReserveDebt vault, address user, bytes32 id, uint256 amount, uint256 epoch) internal {
        bytes32[] memory ids = new bytes32[](1);
        uint256[] memory amounts = new uint256[](1);
        ids[0] = id;
        amounts[0] = amount;
        vm.prank(vault.settlementOperator());
        vault.reportDebts(user, ids, amounts, epoch);
    }

    function _fingerprint(TokenSwoopReserveDebt vault) internal view returns (bytes32) {
        IERC20 asset = vault.reserveToken();
        bytes32 financial = keccak256(
            abi.encode(
                vault.totalPrincipal(),
                vault.totalAssets(),
                vault.aToken().balanceOf(address(vault)),
                asset.balanceOf(address(vault)),
                asset.balanceOf(address(vault.aToken())),
                asset.balanceOf(TREASURY),
                asset.allowance(address(vault), address(vault.aavePool()))
            )
        );
        for (uint256 i; i < 2; ++i) {
            address user = i == 0 ? USER : OTHER;
            financial = keccak256(
                abi.encode(
                    financial,
                    user,
                    asset.balanceOf(user),
                    asset.allowance(user, address(vault)),
                    vault.principalOf(user),
                    vault.debtOf(user),
                    vault.debtEpoch(user),
                    vault.frozenOf(user),
                    vault.withdrawableOf(user)
                )
            );
            for (uint256 j = 1; j <= 3; ++j) {
                (uint256 amount, uint256 epoch, bool reported) = vault.debts(user, bytes32(j));
                financial = keccak256(abi.encode(financial, amount, epoch, reported));
            }
        }
        return keccak256(abi.encode(financial, vault.paused()));
    }

    function testFuzzBatchReportsAreIdempotentAndPartialPaymentsOnlyReleaseTheirItems(uint8 length, uint256 seed)
        public
    {
        uint256 count = bound(length, 1, 64);
        bytes32[] memory ids = new bytes32[](count);
        uint256[] memory amounts = new uint256[](count);
        uint256 total;
        for (uint256 i; i < count; ++i) {
            ids[i] = bytes32(i + 1);
            amounts[i] = uint256(keccak256(abi.encode(seed, i))) % 2e6;
            total += amounts[i];
        }
        vm.startPrank(address(pool));
        reserve.reportDebts(USER, ids, amounts, 0);
        reserve.reportDebts(USER, ids, amounts, 0);
        assertEq(reserve.debtOf(USER), total, "replayed report increased debt");
        uint256 remaining;
        for (uint256 i; i < count; ++i) {
            if (i % 2 == 0) amounts[i] = 0;
            else amounts[i] /= 2;
            remaining += amounts[i];
        }
        reserve.reportDebts(USER, ids, amounts, 0);
        vm.stopPrank();
        assertEq(reserve.debtOf(USER), remaining);
        assertEq(reserve.debtOf(OTHER), 0);
        assertEq(reserve.principalOf(USER), 100e6);
        assertEq(reserve.totalPrincipal(), 170e6);
        assertEq(reserve.frozenOf(USER), remaining < 100e6 ? remaining : 100e6);
        for (uint256 i; i < count; ++i) {
            (uint256 actual, uint256 epoch, bool reported) = reserve.debts(USER, ids[i]);
            assertEq(actual, amounts[i]);
            assertEq(epoch, 0);
            assertTrue(reported);
        }
    }

    function testReportLimitsRejectEmptyOversizedMismatchedAndZeroUser() public {
        bytes32 beforeState = _fingerprint(reserve);
        bytes32[] memory ids = new bytes32[](65);
        uint256[] memory amounts = new uint256[](65);
        for (uint256 i; i < 65; ++i) {
            ids[i] = bytes32(i + 1);
            amounts[i] = 1;
        }
        vm.startPrank(address(pool));
        vm.expectRevert(TokenSwoopReserveDebt.InvalidDebtReport.selector);
        reserve.reportDebts(USER, ids, amounts, 0);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidDebtReport.selector);
        reserve.reportDebts(USER, new bytes32[](0), new uint256[](0), 0);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidDebtReport.selector);
        reserve.reportDebts(USER, new bytes32[](1), new uint256[](2), 0);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidDebtReport.selector);
        reserve.reportDebts(address(0), new bytes32[](1), new uint256[](1), 0);
        vm.stopPrank();
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testBadBatchAfterValidWritesRollsBackAllItems() public {
        _report(reserve, USER, bytes32(uint256(1)), 7e6, 0);
        bytes32 beforeState = _fingerprint(reserve);
        bytes32[] memory ids = new bytes32[](3);
        uint256[] memory amounts = new uint256[](3);
        ids[0] = bytes32(uint256(1));
        ids[1] = bytes32(uint256(2));
        amounts[0] = 2e6;
        amounts[1] = 5e6;
        amounts[2] = 1e6;
        // Zero, descending and duplicate IDs all fail after earlier entries were written.
        for (uint256 last; last <= 2; ++last) {
            ids[2] = bytes32(last);
            vm.expectRevert(TokenSwoopReserveDebt.InvalidDebtReport.selector);
            vm.prank(address(pool));
            reserve.reportDebts(USER, ids, amounts, 0);
            assertEq(_fingerprint(reserve), beforeState);
        }
    }

    function testDebtOverflowRollsBackEarlierItems() public {
        _report(reserve, USER, bytes32(uint256(1)), 7e6, 0);
        bytes32 beforeState = _fingerprint(reserve);
        bytes32[] memory ids = new bytes32[](2);
        uint256[] memory amounts = new uint256[](2);
        ids[0] = bytes32(uint256(1));
        ids[1] = bytes32(uint256(2));
        amounts[0] = type(uint256).max;
        amounts[1] = 1;
        vm.expectRevert(abi.encodeWithSignature("Panic(uint256)", 0x11));
        vm.prank(address(pool));
        reserve.reportDebts(USER, ids, amounts, 0);
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testFuzzMaximumDebtCanBeReleasedWithoutOverflow(uint256 amount) public {
        _report(reserve, USER, bytes32(uint256(1)), amount, 0);
        assertEq(reserve.frozenOf(USER), amount < 100e6 ? amount : 100e6);
        _report(reserve, USER, bytes32(uint256(1)), 0, 0);
        assertEq(reserve.withdrawableOf(USER), 100e6);
    }

    function testManagerClearIsUserScopedAndInvalidatesOldEpochEvenForUnseenIDs() public {
        _report(reserve, USER, bytes32(uint256(1)), 10e6, 0);
        _report(reserve, OTHER, bytes32(uint256(1)), 20e6, 0);
        vm.prank(MANAGER);
        reserve.clearDebt(USER);
        assertEq(reserve.debtOf(USER), 0);
        assertEq(reserve.debtOf(OTHER), 20e6);
        assertEq(reserve.debtEpoch(OTHER), 0);
        bytes32 beforeState = _fingerprint(reserve);
        bytes32[] memory ids = new bytes32[](2);
        uint256[] memory amounts = new uint256[](2);
        ids[0] = bytes32(uint256(1));
        ids[1] = bytes32(uint256(3));
        amounts[0] = 4e6;
        amounts[1] = 3e6;
        vm.startPrank(address(pool));
        vm.expectRevert(TokenSwoopReserveDebt.StaleDebtEpoch.selector);
        reserve.reportDebts(USER, ids, amounts, 0);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserveDebt.DebtReleased.selector, ids[0]));
        reserve.reportDebts(USER, ids, amounts, 1);
        vm.stopPrank();
        assertEq(_fingerprint(reserve), beforeState);
        _report(reserve, USER, bytes32(uint256(2)), 3e6, 1);
        vm.prank(MANAGER);
        reserve.clearDebt(USER);
        _report(reserve, USER, bytes32(uint256(3)), 1e6, 2);
        assertEq(reserve.debtEpoch(USER), 2);
        assertEq(reserve.debtOf(USER), 1e6);
        assertEq(reserve.debtOf(OTHER), 20e6);
    }

    function testLateRetiredIdRevertsNewItemsInSameBatch() public {
        _report(reserve, USER, bytes32(uint256(3)), 8e6, 0);
        vm.prank(MANAGER);
        reserve.clearDebt(USER);
        bytes32 beforeState = _fingerprint(reserve);
        bytes32[] memory ids = new bytes32[](2);
        uint256[] memory amounts = new uint256[](2);
        ids[0] = bytes32(uint256(2));
        ids[1] = bytes32(uint256(3));
        amounts[0] = 4e6;
        amounts[1] = 2e6;
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserveDebt.DebtReleased.selector, ids[1]));
        vm.prank(address(pool));
        reserve.reportDebts(USER, ids, amounts, 1);
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testFrozenDebtSurvivesNewDepositAndDoesNotAffectOtherWallet() public {
        _report(reserve, USER, bytes32(uint256(1)), 120e6, 0);
        assertEq(reserve.withdrawableOf(USER), 0);
        _deposit(reserve, USER, 30e6);
        assertEq(reserve.withdrawableOf(USER), 10e6);
        vm.prank(USER);
        reserve.withdraw(10e6);
        vm.prank(OTHER);
        reserve.withdraw(70e6);
        assertEq(reserve.principalOf(USER), 120e6);
        assertEq(reserve.frozenOf(USER), 120e6);
        assertEq(reserve.principalOf(OTHER), 0);
    }

    function testPauseAllowsWithdrawalReportingRepaymentAndManagerRelease() public {
        vm.prank(MANAGER);
        reserve.setDepositsPaused(true);
        _report(reserve, USER, bytes32(uint256(1)), 20e6, 0);
        vm.prank(USER);
        reserve.withdraw(80e6);
        _report(reserve, USER, bytes32(uint256(1)), 10e6, 0);
        vm.prank(USER);
        reserve.withdraw(10e6);
        vm.prank(MANAGER);
        reserve.clearDebt(USER);
        vm.prank(USER);
        reserve.withdraw(10e6);
        assertTrue(reserve.paused());
        assertEq(reserve.principalOf(USER), 0);
        assertEq(reserve.principalOf(OTHER), 70e6);
    }

    function testUnauthorizedRolesCannotChangeDebtPauseOrTakeYield() public {
        bytes32 beforeState = _fingerprint(reserve);
        bytes32[] memory ids = new bytes32[](1);
        ids[0] = bytes32(uint256(1));
        uint256[] memory amounts = new uint256[](1);
        amounts[0] = 1e6;
        address[3] memory callers = [USER, MANAGER, STRANGER];
        for (uint256 i; i < callers.length; ++i) {
            vm.expectRevert(TokenSwoopReserveDebt.NotOperator.selector);
            vm.prank(callers[i]);
            reserve.reportDebts(USER, ids, amounts, 0);
        }
        callers = [USER, address(pool), STRANGER];
        for (uint256 i; i < callers.length; ++i) {
            vm.startPrank(callers[i]);
            vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
            reserve.clearDebt(USER);
            vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
            reserve.setDepositsPaused(true);
            vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
            reserve.withdrawYield(1);
            vm.stopPrank();
        }
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testFixedRolesHaveNoRotationEntryPoints() public {
        bytes4[3] memory selectors = [
            bytes4(keccak256("setSettlementOperator(address)")),
            bytes4(keccak256("setManager(address)")),
            bytes4(keccak256("transferOwnership(address)"))
        ];
        for (uint256 i; i < selectors.length; ++i) {
            vm.prank(MANAGER);
            (bool ok,) = address(reserve).call(abi.encodeWithSelector(selectors[i], STRANGER));
            assertFalse(ok);
        }
        assertEq(reserve.manager(), MANAGER);
        assertEq(reserve.settlementOperator(), address(pool));
    }

    function testPoolOperatorCannotReenterDebtReportDuringDeposit() public {
        bytes32[] memory ids = new bytes32[](1);
        ids[0] = bytes32(uint256(1));
        uint256[] memory amounts = new uint256[](1);
        amounts[0] = 25e6;
        pool.configure(address(reserve), abi.encodeCall(reserve.reportDebts, (USER, ids, amounts, 0)), false, false);
        _deposit(reserve, OTHER, 10e6);
        assertEq(pool.callbackAttempts(), 1);
        assertFalse(pool.callbackSucceeded());
        assertEq(pool.callbackResult(), abi.encodeWithSelector(ReentrancyGuard.ReentrancyGuardReentrantCall.selector));
        assertEq(reserve.debtOf(USER), 0);
        assertEq(reserve.totalPrincipal(), 180e6);
    }

    function testPoolManagerCannotReenterClearDebtDuringWithdrawal() public {
        TokenSwoopReserveDebt vault =
            new TokenSwoopReserveDebt(address(token), address(pool), address(pool), MANAGER, TREASURY);
        _deposit(vault, OTHER, 20e6);
        _deposit(vault, USER, 20e6);
        _report(vault, OTHER, bytes32(uint256(1)), 10e6, 0);
        pool.configure(address(vault), abi.encodeCall(vault.clearDebt, (OTHER)), false, false);
        vm.prank(USER);
        vault.withdraw(20e6);
        assertEq(pool.callbackAttempts(), 1);
        assertFalse(pool.callbackSucceeded());
        assertEq(pool.callbackResult(), abi.encodeWithSelector(ReentrancyGuard.ReentrancyGuardReentrantCall.selector));
        assertEq(vault.debtOf(OTHER), 10e6);
        assertEq(vault.debtEpoch(OTHER), 0);
        assertEq(vault.principalOf(USER), 0);
    }

    function testShortPoolReturnRollsBackTransfersPrincipalDebtAndReceipts() public {
        _report(reserve, USER, bytes32(uint256(1)), 25e6, 0);
        pool.configure(address(0), "", true, false);
        bytes32 beforeState = _fingerprint(reserve);
        vm.expectRevert(TokenSwoopReserveDebt.UnexpectedPoolWithdrawal.selector);
        vm.prank(USER);
        reserve.withdraw(75e6);
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testMissingPoolReceiptCannotCreateUnbackedPrincipal() public {
        vm.prank(OTHER);
        token.approve(address(reserve), 10e6);
        pool.configure(address(0), "", false, true);
        bytes32 beforeState = _fingerprint(reserve);
        vm.expectRevert(TokenSwoopReserveDebt.InsufficientBacking.selector);
        vm.prank(OTHER);
        reserve.deposit(10e6);
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testFuzzYieldCannotConsumePrincipalEvenWhenAllPrincipalIsFrozen(uint96 seed) public {
        uint256 yieldAmount = bound(uint256(seed), 1, 1000e6);
        _report(reserve, USER, bytes32(uint256(1)), 100e6, 0);
        _report(reserve, OTHER, bytes32(uint256(1)), 70e6, 0);
        token.mint(address(reserve), yieldAmount);
        assertEq(reserve.availableYield(), yieldAmount);
        vm.expectRevert(TokenSwoopReserveDebt.ExcessYieldWithdrawal.selector);
        vm.prank(MANAGER);
        reserve.withdrawYield(yieldAmount + 1);
        vm.prank(MANAGER);
        reserve.withdrawYield(yieldAmount);
        assertEq(token.balanceOf(TREASURY), yieldAmount);
        assertEq(reserve.totalAssets(), 171e6);
        assertEq(reserve.totalPrincipal(), 170e6);
        assertEq(reserve.frozenOf(USER) + reserve.frozenOf(OTHER), 170e6);
        assertEq(reserve.availableYield(), 0);
    }

    function _hostileTokenVault()
        internal
        returns (ReserveDebtTokenMock asset, CallbackPool p, TokenSwoopReserveDebt vault)
    {
        asset = new ReserveDebtTokenMock();
        p = new CallbackPool(address(asset));
        vault = new TokenSwoopReserveDebt(address(asset), address(p), MANAGER, address(asset), TREASURY);
        asset.mint(USER, 100e6);
        vm.prank(USER);
        asset.approve(address(vault), 100e6);
    }

    function testFalseReturningTokenCannotCreatePrincipalOrBuffer() public {
        (ReserveDebtTokenMock asset,, TokenSwoopReserveDebt vault) = _hostileTokenVault();
        asset.configure(true, false, address(0));
        bytes32 beforeState = _fingerprint(vault);
        vm.startPrank(USER);
        vm.expectRevert(abi.encodeWithSelector(SafeERC20.SafeERC20FailedOperation.selector, address(asset)));
        vault.deposit(50e6);
        vm.expectRevert(abi.encodeWithSelector(SafeERC20.SafeERC20FailedOperation.selector, address(asset)));
        vault.fundBuffer(1e6);
        vm.stopPrank();
        assertEq(_fingerprint(vault), beforeState);
    }

    function testFeeOnTransferTokenCannotCreatePrincipalOrBuffer() public {
        (ReserveDebtTokenMock asset,, TokenSwoopReserveDebt vault) = _hostileTokenVault();
        asset.configure(false, true, address(0));
        bytes32 beforeState = _fingerprint(vault);
        vm.startPrank(USER);
        vm.expectRevert(TokenSwoopReserveDebt.UnsupportedTokenTransfer.selector);
        vault.deposit(50e6);
        vm.expectRevert(TokenSwoopReserveDebt.UnsupportedTokenTransfer.selector);
        vault.fundBuffer(1e6);
        vm.stopPrank();
        assertEq(_fingerprint(vault), beforeState);
    }

    function testRecipientTransferFailureRollsBackPoolRedemption() public {
        (ReserveDebtTokenMock asset,, TokenSwoopReserveDebt vault) = _hostileTokenVault();
        vm.prank(USER);
        vault.deposit(100e6);
        _report(vault, USER, bytes32(uint256(1)), 30e6, 0);
        asset.configure(false, false, USER);
        bytes32 beforeState = _fingerprint(vault);
        vm.expectRevert(abi.encodeWithSelector(SafeERC20.SafeERC20FailedOperation.selector, address(asset)));
        vm.prank(USER);
        vault.withdraw(70e6);
        assertEq(_fingerprint(vault), beforeState);
    }

    function testTokenOperatorCannotReenterDuringTransferFrom() public {
        (ReserveDebtTokenMock asset,, TokenSwoopReserveDebt vault) = _hostileTokenVault();
        bytes32[] memory ids = new bytes32[](1);
        ids[0] = bytes32(uint256(1));
        uint256[] memory amounts = new uint256[](1);
        amounts[0] = 10e6;
        asset.setCallback(address(vault), abi.encodeCall(vault.reportDebts, (USER, ids, amounts, 0)));
        vm.prank(USER);
        vault.deposit(50e6);
        assertEq(asset.callbackAttempts(), 2);
        assertFalse(asset.callbackSucceeded());
        assertEq(asset.callbackResult(), abi.encodeWithSelector(ReentrancyGuard.ReentrancyGuardReentrantCall.selector));
        assertEq(vault.debtOf(USER), 0);
        assertEq(vault.principalOf(USER), 50e6);
        assertEq(asset.allowance(address(vault), address(vault.aavePool())), 0);
    }

    function testPausedOrBlacklistedTokenCannotCorruptWithdrawalAccounting() public {
        _report(reserve, USER, bytes32(uint256(1)), 30e6, 0);
        bytes32 beforeState = _fingerprint(reserve);
        token.pause();
        vm.expectRevert(Pausable.EnforcedPause.selector);
        vm.prank(USER);
        reserve.withdraw(70e6);
        assertEq(_fingerprint(reserve), beforeState);
        token.unpause();
        token.blacklist(USER);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, USER));
        vm.prank(USER);
        reserve.withdraw(70e6);
        assertEq(_fingerprint(reserve), beforeState);
        token.unBlacklist(USER);
        vm.prank(USER);
        reserve.withdraw(70e6);
        assertEq(reserve.principalOf(USER), 30e6);
        assertEq(reserve.frozenOf(USER), 30e6);
    }

    function testConstructorRejectsInvalidRolesAndMissingContracts() public {
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(0), address(pool), MANAGER, USER, TREASURY);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), STRANGER, MANAGER, USER, TREASURY);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), address(0), USER, TREASURY);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, address(0), TREASURY);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, MANAGER, TREASURY);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, USER, address(0));
    }

    function testZeroAmountsAndZeroUserAreRejectedWithoutMutation() public {
        bytes32 beforeState = _fingerprint(reserve);
        vm.startPrank(USER);
        vm.expectRevert(TokenSwoopReserveDebt.ZeroAmount.selector);
        reserve.deposit(0);
        vm.expectRevert(TokenSwoopReserveDebt.ZeroAmount.selector);
        reserve.withdraw(0);
        vm.expectRevert(TokenSwoopReserveDebt.ZeroAmount.selector);
        reserve.fundBuffer(0);
        vm.stopPrank();
        vm.startPrank(MANAGER);
        vm.expectRevert(TokenSwoopReserveDebt.ZeroAmount.selector);
        reserve.withdrawYield(0);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        reserve.clearDebt(address(0));
        vm.stopPrank();
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testInsufficientAllowanceOrBalanceDoesNotCreatePrincipal() public {
        vm.prank(USER);
        token.approve(address(reserve), 1e6);
        bytes32 beforeState = _fingerprint(reserve);
        vm.expectRevert(
            abi.encodeWithSignature("ERC20InsufficientAllowance(address,uint256,uint256)", address(reserve), 1e6, 2e6)
        );
        vm.prank(USER);
        reserve.deposit(2e6);
        assertEq(_fingerprint(reserve), beforeState);
        vm.prank(USER);
        token.approve(address(reserve), 101e6);
        beforeState = _fingerprint(reserve);
        vm.expectRevert(
            abi.encodeWithSignature("ERC20InsufficientBalance(address,uint256,uint256)", USER, 100e6, 101e6)
        );
        vm.prank(USER);
        reserve.deposit(101e6);
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testIdleLiquidityCanBeWithdrawnWhilePoolIsUnavailable() public {
        token.mint(address(reserve), 10e6);
        vm.mockCallRevert(
            address(pool), abi.encodeWithSignature("withdraw(address,uint256,address)"), bytes("pool offline")
        );
        vm.prank(USER);
        reserve.withdraw(5e6);
        assertEq(reserve.principalOf(USER), 95e6);
        assertEq(token.balanceOf(USER), 105e6);
        bytes32 beforeState = _fingerprint(reserve);
        vm.expectRevert(bytes("pool offline"));
        vm.prank(USER);
        reserve.withdraw(95e6);
        assertEq(_fingerprint(reserve), beforeState);
    }

    function testConstructorRejectsWrongTokenDecimalsOrReceiptBindings() public {
        TestPaymentToken wrong = new TestPaymentToken(18);
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(wrong), address(pool), MANAGER, USER, TREASURY);
        vm.mockCall(address(pool.receipt()), abi.encodeWithSignature("POOL()"), abi.encode(STRANGER));
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, USER, TREASURY);
        vm.clearMockedCalls();
        vm.mockCall(
            address(pool.receipt()), abi.encodeWithSignature("UNDERLYING_ASSET_ADDRESS()"), abi.encode(STRANGER)
        );
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, USER, TREASURY);
        vm.clearMockedCalls();
        vm.mockCall(address(pool.receipt()), abi.encodeWithSignature("decimals()"), abi.encode(uint8(18)));
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, USER, TREASURY);
        vm.clearMockedCalls();
        vm.mockCall(
            address(pool), abi.encodeWithSignature("getReserveAToken(address)", address(token)), abi.encode(address(0))
        );
        vm.expectRevert(TokenSwoopReserveDebt.InvalidConfiguration.selector);
        new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, USER, TREASURY);
    }
}
