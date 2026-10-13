// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {IERC20Errors} from "@openzeppelin/contracts/interfaces/draft-IERC6093.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TestPaymentToken, NoMetadataToken} from "./helpers/Mocks.sol";

contract TokenSwoopPayGTest is Test {
    KUSDC internal token;
    TokenSwoopPayG internal payg;
    address internal user;
    address internal provider;
    address internal otherProvider;
    bytes32 internal constant BILL = keccak256("bill-1");

    event Charged(address indexed user, address indexed provider, bytes32 indexed billId, uint256 amount);

    function setUp() public {
        vm.warp(1_000_000);
        user = makeAddr("user");
        provider = makeAddr("provider");
        otherProvider = makeAddr("other-provider");
        token = new KUSDC(address(this));
        payg = new TokenSwoopPayG(address(token), address(this), provider, provider);
        token.mint(user, 100e6);
        vm.prank(user);
        token.approve(address(payg), 10e6);
    }

    function _charge(bytes32 billId, uint256 amount) internal {
        vm.prank(provider);
        payg.charge(user, billId, amount);
    }

    function _state(bytes32 billId) internal view returns (bytes32) {
        return keccak256(
            abi.encode(
                payg.isCharged(user, billId),
                token.balanceOf(user),
                token.balanceOf(provider),
                token.balanceOf(address(payg)),
                token.allowance(user, address(payg))
            )
        );
    }

    function testApproveAloneEnablesPaymentWithAccurateBalancesAllowanceAndEvent() public {
        vm.expectEmit(true, true, true, true, address(payg));
        emit Charged(user, provider, BILL, 4200);
        _charge(BILL, 4200);
        assertEq(token.balanceOf(user), 100e6 - 4200);
        assertEq(token.balanceOf(provider), 4200);
        assertEq(token.allowance(user, address(payg)), 10e6 - 4200);
        assertEq(token.balanceOf(address(payg)), 0);
        assertTrue(payg.isCharged(user, BILL));
        assertEq(address(payg.paymentToken()), address(token));
    }

    function testRejectsTokenWithoutCode() public {
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.InvalidPaymentToken.selector, address(0)));
        new TokenSwoopPayG(address(0), address(this), provider, provider);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.InvalidPaymentToken.selector, user));
        new TokenSwoopPayG(user, address(this), provider, provider);
    }

    function testRejectsTokenWithoutMetadata() public {
        address invalid = address(new NoMetadataToken());
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.InvalidPaymentToken.selector, invalid));
        new TokenSwoopPayG(invalid, address(this), provider, provider);
    }

    function testRejectsWrongDecimals() public {
        TestPaymentToken invalid = new TestPaymentToken(18);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.UnsupportedDecimals.selector, uint8(18)));
        new TokenSwoopPayG(address(invalid), address(this), provider, provider);
    }

    function testRejectsZeroOwner() public {
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableInvalidOwner.selector, address(0)));
        new TokenSwoopPayG(address(token), address(0), provider, provider);
    }

    function testRejectsZeroProvider() public {
        vm.expectRevert(TokenSwoopPayG.InvalidProvider.selector);
        new TokenSwoopPayG(address(token), address(this), address(0), address(0));
    }

    function testNoTokenAllowanceRejectsPaymentAndCanRetrySameId() public {
        vm.prank(user);
        token.approve(address(payg), 0);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 4200)
        );
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
        vm.prank(user);
        token.approve(address(payg), 4200);
        _charge(BILL, 4200);
        assertEq(token.allowance(user, address(payg)), 0);
        assertTrue(payg.isCharged(user, BILL));
    }

    function testApprovingProviderDirectlyDoesNotAuthorizePaymentContract() public {
        vm.startPrank(user);
        token.approve(address(payg), 0);
        token.approve(provider, 10e6);
        vm.stopPrank();
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 4200)
        );
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
        assertEq(token.allowance(user, provider), 10e6);
    }

    function testCannotUseAnotherUsersAllowance() public {
        address secondUser = makeAddr("another-user");
        token.mint(secondUser, 100e6);
        vm.prank(provider);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 4200)
        );
        payg.charge(secondUser, BILL, 4200);
        assertEq(token.allowance(user, address(payg)), 10e6);
        assertEq(token.balanceOf(secondUser), 100e6);
        assertFalse(payg.isCharged(secondUser, BILL));
    }

    function testRejectsZeroCharge() public {
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(TokenSwoopPayG.ZeroCharge.selector);
        _charge(BILL, 0);
        assertEq(_state(BILL), beforeState);
    }

    function testRejectsZeroUser() public {
        vm.prank(provider);
        vm.expectRevert(TokenSwoopPayG.InvalidUser.selector);
        payg.charge(address(0), BILL, 1);
    }

    function testAllowanceHasNoPaymentContractExpiry() public {
        vm.warp(block.timestamp + 365 days);
        _charge(BILL, 4200);
        assertTrue(payg.isCharged(user, BILL));
    }

    function testWholeAllowanceCanBeSpentInOneCharge() public {
        _charge(BILL, 10e6);
        assertEq(token.balanceOf(provider), 10e6);
        assertEq(token.allowance(user, address(payg)), 0);
    }

    function testCompetingBillsCannotExceedRemainingAllowance() public {
        vm.prank(user);
        token.approve(address(payg), 1e6);
        _charge(BILL, 600_000);
        bytes32 second = keccak256("bill-2");
        bytes32 beforeState = _state(second);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 400_000, 600_000)
        );
        _charge(second, 600_000);
        assertEq(_state(second), beforeState);
        _charge(keccak256("bill-3"), 400_000);
        assertEq(token.allowance(user, address(payg)), 0);
        assertEq(token.balanceOf(provider), 1e6);
        assertFalse(payg.isCharged(user, second));
    }

    function testRejectsDuplicateBillWithoutConsumingAllowance() public {
        _charge(BILL, 4200);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector, BILL));
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
    }

    function testRevokingAndReapprovingCannotReplayPaidId() public {
        _charge(BILL, 4200);
        vm.startPrank(user);
        token.approve(address(payg), 0);
        token.approve(address(payg), 20e6);
        vm.stopPrank();
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector, BILL));
        _charge(BILL, 8400);
        assertEq(_state(BILL), beforeState);
    }

    function testReapprovalSetsRemainingAllowanceInsteadOfLifetimeBudget() public {
        _charge(BILL, 3e6);
        assertEq(token.allowance(user, address(payg)), 7e6);
        vm.prank(user);
        token.approve(address(payg), 10e6);
        _charge(keccak256("next-bill"), 10e6);
        assertEq(token.balanceOf(provider), 13e6);
        assertEq(token.allowance(user, address(payg)), 0);
    }

    function testQueuedUnpaidBillCanBeChargedAfterReapproval() public {
        vm.prank(user);
        token.approve(address(payg), 0);
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 4200)
        );
        _charge(BILL, 4200);
        assertFalse(payg.isCharged(user, BILL));
        vm.prank(user);
        token.approve(address(payg), 10e6);
        _charge(BILL, 4200);
        assertTrue(payg.isCharged(user, BILL));
    }

    function testIncreasingAndDecreasingTokenAllowanceImmediatelyAffectsSpending() public {
        vm.startPrank(user);
        token.decreaseAllowance(address(payg), 9e6);
        token.increaseAllowance(address(payg), 2e6);
        vm.stopPrank();
        _charge(BILL, 3e6);
        assertEq(token.allowance(user, address(payg)), 0);
    }

    function testOnlyFixedProviderCanChargeIncludingOwnerAndUser() public {
        bytes32 beforeState = _state(BILL);
        address[3] memory callers = [otherProvider, user, address(this)];
        for (uint256 i; i < callers.length; ++i) {
            vm.prank(callers[i]);
            vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.NotProvider.selector, callers[i]));
            payg.charge(user, BILL, 4200);
            assertEq(_state(BILL), beforeState);
        }
        assertEq(payg.provider(), provider);
    }

    function testUsersHaveIndependentAllowancesAndBillNamespaces() public {
        address secondUser = makeAddr("second-user");
        token.mint(secondUser, 50e6);
        vm.prank(secondUser);
        token.approve(address(payg), 5e6);
        _charge(BILL, 4200);
        vm.prank(provider);
        payg.charge(secondUser, BILL, 8400);
        assertEq(token.allowance(user, address(payg)), 10e6 - 4200);
        assertEq(token.allowance(secondUser, address(payg)), 5e6 - 8400);
        assertTrue(payg.isCharged(user, BILL));
        assertTrue(payg.isCharged(secondUser, BILL));
    }

    function testInsufficientBalanceRollsBackAllowanceAndSameIdCanBeRetried() public {
        vm.prank(user);
        token.transfer(address(this), 100e6);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(IERC20Errors.ERC20InsufficientBalance.selector, user, 0, 4200));
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
        token.mint(user, 4200);
        _charge(BILL, 4200);
        assertTrue(payg.isCharged(user, BILL));
    }

    function testTokenPauseRollsBackPayment() public {
        token.pause();
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
        token.unpause();
        _charge(BILL, 4200);
    }

    function testBlacklistedUserRollsBackPayment() public {
        token.blacklist(user);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, user));
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
    }

    function testBlacklistedProviderRollsBackPayment() public {
        token.blacklist(provider);
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, provider));
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
    }

    function testBlacklistedPaymentContractRollsBackPayment() public {
        token.blacklist(address(payg));
        bytes32 beforeState = _state(BILL);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, address(payg)));
        _charge(BILL, 4200);
        assertEq(_state(BILL), beforeState);
    }

    function testAdminPauseBlocksChargesButTokenAllowanceCanBeRevoked() public {
        payg.pause();
        vm.expectRevert(Pausable.EnforcedPause.selector);
        _charge(BILL, 4200);
        vm.prank(user);
        token.approve(address(payg), 0);
        payg.unpause();
        vm.expectRevert(
            abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, address(payg), 0, 4200)
        );
        _charge(BILL, 4200);
    }

    function testTokenAllowanceCanBeChangedWhilePaymentPaused() public {
        payg.pause();
        vm.prank(user);
        token.approve(address(payg), 1e6);
        payg.unpause();
        _charge(BILL, 1e6);
        assertEq(token.allowance(user, address(payg)), 0);
    }

    function testOnlyOwnerCanPauseOrUnpause() public {
        vm.prank(provider);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, provider));
        payg.pause();
        payg.pause();
        vm.prank(user);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user));
        payg.unpause();
    }

    function testStopAuthorityAndProviderAreFixed() public {
        vm.expectRevert(TokenSwoopPayG.FixedStopAuthority.selector);
        payg.transferOwnership(otherProvider);
        assertEq(payg.owner(), address(this));
        vm.expectRevert(TokenSwoopPayG.FixedStopAuthority.selector);
        payg.renounceOwnership();
        assertEq(payg.stopAuthority(), address(this));
        assertEq(payg.provider(), provider);
        vm.prank(otherProvider);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.NotProvider.selector, otherProvider));
        payg.charge(user, BILL, 4200);
        _charge(BILL, 4200);
    }

    function testSafeERC20FalseReturnRollsBack() public {
        TestPaymentToken failingToken = new TestPaymentToken(6);
        TokenSwoopPayG failingPayg = new TokenSwoopPayG(address(failingToken), address(this), provider, provider);
        failingToken.mint(user, 100e6);
        failingToken.configure(true, TokenSwoopPayG(address(0)));
        vm.prank(user);
        failingToken.approve(address(failingPayg), 10e6);
        vm.prank(provider);
        vm.expectRevert(abi.encodeWithSelector(SafeERC20.SafeERC20FailedOperation.selector, address(failingToken)));
        failingPayg.charge(user, BILL, 4200);
        assertFalse(failingPayg.isCharged(user, BILL));
        assertEq(failingToken.balanceOf(user), 100e6);
        assertEq(failingToken.allowance(user, address(failingPayg)), 10e6);
        assertEq(failingToken.transferFromCalls(), 0);
    }

    function testReentrantProviderContractCannotChargeAgain() public {
        TestPaymentToken maliciousToken = new TestPaymentToken(6);
        TokenSwoopPayG guarded = new TokenSwoopPayG(address(maliciousToken), address(this), address(maliciousToken), address(maliciousToken));
        maliciousToken.mint(user, 100e6);
        maliciousToken.configure(false, guarded);
        vm.prank(user);
        maliciousToken.approve(address(guarded), 10e6);
        vm.prank(address(maliciousToken));
        guarded.charge(user, BILL, 4200);
        assertTrue(maliciousToken.attemptedReentry());
        assertFalse(maliciousToken.reentrySucceeded());
        assertFalse(guarded.isCharged(user, keccak256("reentrant")));
        assertEq(maliciousToken.balanceOf(address(maliciousToken)), 4200);
        assertEq(maliciousToken.allowance(user, address(guarded)), 10e6 - 4200);
    }

    function testMaximumUSDCAllowanceStillDecrements() public {
        vm.prank(user);
        token.approve(address(payg), type(uint256).max);
        _charge(BILL, 4200);
        assertEq(token.allowance(user, address(payg)), type(uint256).max - 4200);
    }

    function testLegacyAuthorizationAndChargeInterfacesRejectCalls() public {
        bytes32 beforeState = _state(BILL);
        vm.startPrank(user);
        (bool grantSuccess,) = address(payg)
            .call(abi.encodeWithSignature("authorizeProvider(uint256,uint64)", 10e6, uint64(block.timestamp + 1 days)));
        (bool revokeSuccess,) = address(payg).call(abi.encodeWithSignature("revokeProvider()"));
        vm.stopPrank();
        vm.prank(provider);
        (bool chargeSuccess,) =
            address(payg).call(abi.encodeWithSignature("charge(address,uint256,bytes32,uint256)", user, 1, BILL, 4200));
        assertFalse(grantSuccess);
        assertFalse(revokeSuccess);
        assertFalse(chargeSuccess);
        assertEq(_state(BILL), beforeState);
    }

    function testFuzzSuccessfulPaymentConservesFunds(uint96 rawAmount) public {
        uint256 amount = bound(uint256(rawAmount), 1, 10e6);
        _charge(BILL, amount);
        assertEq(token.balanceOf(user) + token.balanceOf(provider), 100e6);
        assertEq(token.allowance(user, address(payg)) + amount, 10e6);
        assertTrue(payg.isCharged(user, BILL));
    }
}
