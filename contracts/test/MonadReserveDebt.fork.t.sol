// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {TokenSwoopReserveDebt} from "../src/TokenSwoopReserveDebt.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";
import {DeployReserveDebtMainnet} from "../script/DeployReserveDebt.s.sol";

interface ICircleControls {
    function blacklister() external view returns (address);
    function blacklist(address) external;
    function unBlacklist(address) external;
    function pauser() external view returns (address);
    function pause() external;
    function unpause() external;
}

/// @dev READ-ONLY RPC: deployments, balances, role impersonation and transactions
/// happen only inside the local fork. This suite never broadcasts to mainnet.
contract MonadReserveDebtForkTest is Test {
    address constant USDC = 0x754704Bc059F8C67012fEd69BC8A327a5aafb603;
    address constant POOL = 0x69a5F9AD4f96ebf0a0C792dD42a01cC5C0102fef;
    address constant A_TOKEN = 0x35a73BAcb179d3740395A3ceCc87FF2e581d6042;
    TokenSwoopReserveDebt reserve;
    TokenSwoopPayGBatch payment;
    address user;
    address operator;
    address treasury;
    bool enabled;

    function setUp() public {
        enabled = vm.envOr("RUN_MAINNET_FORK", false);
        if (!enabled) return;
        string memory rpc = vm.envString("MONAD_MAINNET_RPC_URL");
        uint256 pinnedBlock = vm.envUint("MAINNET_FORK_BLOCK");
        vm.createSelectFork(rpc, pinnedBlock);
        assertEq(block.chainid, 143);
        user = makeAddr("debt-mainnet-fork-user");
        operator = makeAddr("debt-mainnet-fork-operator");
        treasury = makeAddr("debt-mainnet-fork-treasury");
        reserve = new TokenSwoopReserveDebt(USDC, POOL, address(this), operator, treasury);
        payment = new TokenSwoopPayGBatch(USDC, address(this), operator, operator);
        deal(USDC, user, 2000e6);
        deal(USDC, address(this), 1e6);
        IERC20(USDC).approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
        vm.startPrank(user);
        IERC20(USDC).approve(address(reserve), 1000e6);
        IERC20(USDC).approve(address(payment), 100e6);
        vm.stopPrank();
    }

    modifier forkOnly() {
        if (!enabled) vm.skip(true);
        _;
    }

    function deposit(uint256 amount) internal {
        vm.prank(user);
        reserve.deposit(amount);
    }

    function report(uint256 amount, uint256 epoch) internal {
        bytes32[] memory ids = new bytes32[](1);
        uint256[] memory amounts = new uint256[](1);
        ids[0] = keccak256("completed-request");
        amounts[0] = amount;
        vm.prank(operator);
        reserve.reportDebts(user, ids, amounts, epoch);
    }

    function testForkCurrentDebtContractBindsOfficialAaveUSDC() public forkOnly {
        assertEq(address(reserve.reserveToken()), USDC);
        assertEq(address(reserve.aavePool()), POOL);
        assertEq(address(reserve.aToken()), A_TOKEN);
        assertEq(reserve.manager(), address(this));
        assertEq(reserve.settlementOperator(), operator);
        assertEq(reserve.treasury(), treasury);
    }

    function testForkDepositPreservesPaymentAllowanceAndClearsPoolAllowance() public forkOnly {
        deposit(50e6);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(IERC20(USDC).allowance(user, address(payment)), 100e6);
        assertEq(IERC20(USDC).allowance(user, address(reserve)), 950e6);
        assertEq(IERC20(USDC).allowance(address(reserve), POOL), 0);
        assertGe(reserve.totalAssets(), reserve.totalPrincipal());
    }

    function testForkDirectFullWithdrawalNeedsNoOperatorApproval() public forkOnly {
        deposit(1000e6);
        vm.prank(user);
        reserve.withdraw(1000e6);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
        assertEq(reserve.principalOf(user), 0);
        assertEq(reserve.totalPrincipal(), 0);
    }

    function testForkFailedPaymentFreezesOnlyDebtAndExactRetryPaysOnce() public forkOnly {
        deposit(50e6);
        vm.prank(user);
        IERC20(USDC).approve(address(payment), 0);
        bytes32 bill = keccak256("completed-bill");
        vm.prank(operator);
        vm.expectRevert();
        payment.chargeBatch(user, bill, 5e6);
        report(5e6, 0); // Worker reports confirmed failed settlement.
        assertEq(reserve.withdrawableOf(user), 45e6);
        vm.prank(user);
        reserve.withdraw(45e6);
        vm.prank(user);
        IERC20(USDC).approve(address(payment), 5e6);
        vm.prank(operator);
        assertEq(payment.chargeBatch(user, bill, 5e6), 5e6);
        vm.prank(operator);
        vm.expectRevert(abi.encodeWithSignature("RequestAlreadyCharged(bytes32)", bill));
        payment.chargeBatch(user, bill, 5e6);
        report(0, 0); // Reconciled payment releases its remaining debt.
        vm.prank(user);
        reserve.withdraw(5e6);
        assertEq(IERC20(USDC).balanceOf(user), 1995e6);
        assertEq(IERC20(USDC).balanceOf(operator), 5e6);
        assertEq(reserve.totalPrincipal(), 0);
    }

    function testForkManagerReleaseCannotBeOverwrittenByOldReport() public forkOnly {
        deposit(50e6);
        report(70e6, 0);
        assertEq(reserve.frozenOf(user), 50e6);
        reserve.clearDebt(user);
        vm.expectRevert(TokenSwoopReserveDebt.StaleDebtEpoch.selector);
        report(70e6, 0);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserveDebt.DebtReleased.selector, keccak256("completed-request")));
        report(70e6, 1);
        vm.prank(user);
        reserve.withdraw(50e6);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
    }

    function testForkStoppedChargesAndPausedDepositsStillAllowWithdrawal() public forkOnly {
        deposit(50e6);
        payment.pause();
        reserve.setDepositsPaused(true);
        vm.prank(operator);
        vm.expectRevert();
        payment.chargeBatch(user, keccak256("paused-bill"), 1e6);
        vm.prank(user);
        vm.expectRevert(TokenSwoopReserveDebt.DepositsPaused.selector);
        reserve.deposit(1e6);
        vm.prank(user);
        reserve.withdraw(50e6);
        payment.unpause();
        reserve.setDepositsPaused(false);
        deposit(50e6);
        assertEq(reserve.principalOf(user), 50e6);
    }

    function testForkYieldAfterRealInterestAccrualProtectsPrincipal() public forkOnly {
        deposit(1000e6);
        vm.warp(block.timestamp + 30 days);
        uint256 yield = reserve.availableYield();
        assertGt(yield, 0);
        reserve.withdrawYield(yield);
        assertEq(IERC20(USDC).balanceOf(treasury), yield);
        assertGe(reserve.totalAssets(), 1000e6);
        vm.prank(user);
        reserve.withdraw(1000e6);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
    }

    function testForkPoolLiquidityFailureRollsBackAndRetriesWithoutDoubleDebit() public forkOnly {
        deposit(50e6);
        uint256 liquidity = IERC20(USDC).balanceOf(A_TOKEN);
        deal(USDC, A_TOKEN, 0);
        vm.prank(user);
        vm.expectRevert();
        reserve.withdraw(50e6);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(IERC20(USDC).balanceOf(user), 1950e6);
        deal(USDC, A_TOKEN, liquidity);
        vm.prank(user);
        reserve.withdraw(50e6);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
    }

    function testForkCircleBlacklistRollsBackPrincipalUntilIssuerReleases() public forkOnly {
        deposit(50e6);
        ICircleControls circle = ICircleControls(USDC);
        address blacklister = circle.blacklister();
        vm.prank(blacklister);
        circle.blacklist(user);
        vm.prank(user);
        vm.expectRevert();
        reserve.withdraw(50e6);
        assertEq(reserve.principalOf(user), 50e6);
        vm.prank(blacklister);
        circle.unBlacklist(user);
        vm.prank(user);
        reserve.withdraw(50e6);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
    }

    function testForkCirclePauseFailsAtomicallyAndRecovers() public forkOnly {
        deposit(50e6);
        ICircleControls circle = ICircleControls(USDC);
        address pauser = circle.pauser();
        vm.prank(pauser);
        circle.pause();
        vm.prank(user);
        vm.expectRevert();
        reserve.withdraw(50e6);
        assertEq(reserve.principalOf(user), 50e6);
        vm.prank(pauser);
        circle.unpause();
        vm.prank(user);
        reserve.withdraw(50e6);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
    }

}
