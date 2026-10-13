// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {TokenSwoopReserve} from "../src/TokenSwoopReserve.sol";
import {IAaveV3Pool, IAaveAToken} from "../src/interfaces/IAaveV3Pool.sol";

/// @dev READ-ONLY RPC. Every deployment, deal, approval and withdrawal is on a local fork.
///      Addresses pinned to aave-dao/aave-address-book f648e5dd3763467b836dfa35484ffe7024e6572a.
contract MonadAaveForkTest is Test {
    address internal constant USDC = 0x754704Bc059F8C67012fEd69BC8A327a5aafb603;
    address internal constant POOL = 0x69a5F9AD4f96ebf0a0C792dD42a01cC5C0102fef;
    address internal constant A_TOKEN = 0x35a73BAcb179d3740395A3ceCc87FF2e581d6042;
    address internal user;
    address internal operator;
    address internal treasury;
    TokenSwoopReserve internal reserve;
    bool internal enabled;

    function setUp() public {
        enabled = vm.envOr("RUN_MAINNET_FORK", false);
        if (!enabled) return;
        string memory rpc = vm.envOr("MONAD_MAINNET_RPC_URL", string("https://rpc.monad.xyz"));
        uint256 pinnedBlock = vm.envOr("MAINNET_FORK_BLOCK", uint256(0));
        if (pinnedBlock == 0) vm.createSelectFork(rpc);
        else vm.createSelectFork(rpc, pinnedBlock);
        assertEq(block.chainid, 143);
        user = makeAddr("reserve-fork-user");
        operator = makeAddr("reserve-fork-gateway");
        treasury = makeAddr("reserve-fork-treasury");
        reserve = new TokenSwoopReserve(USDC, POOL, address(this), operator, treasury);
        deal(USDC, user, 2000e6);
        deal(USDC, address(this), 1e6);
        IERC20(USDC).approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
        vm.prank(user);
        IERC20(USDC).approve(address(reserve), 2000e6);
    }

    modifier forkOnly() {
        if (!enabled) vm.skip(true);
        _;
    }

    function _deposit(uint256 amount) internal {
        vm.prank(user);
        reserve.deposit(amount);
    }

    function _requestAndApprove(uint256 amount) internal returns (uint256 id) {
        vm.prank(user);
        id = reserve.requestWithdrawal(amount);
        vm.prank(operator);
        reserve.approveWithdrawal(user, id, uint64(block.timestamp + 1 days));
    }

    function testForkOfficialPoolAndATokenMatchConfiguredNativeUSDC() public forkOnly {
        assertEq(IAaveV3Pool(POOL).getReserveAToken(USDC), A_TOKEN);
        assertEq(IAaveAToken(A_TOKEN).POOL(), POOL);
        assertEq(IAaveAToken(A_TOKEN).UNDERLYING_ASSET_ADDRESS(), USDC);
        assertEq(address(reserve.aToken()), A_TOKEN);
    }

    function testForkRealAaveSupplyCreditsNominalPrincipalWithPlatformRoundingBuffer() public forkOnly {
        _deposit(50e6);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(IERC20(USDC).balanceOf(user), 1950e6);
        assertEq(IERC20(USDC).allowance(user, address(reserve)), 1950e6);
        assertEq(IERC20(USDC).allowance(address(reserve), POOL), 0);
        assertGe(reserve.totalAssets(), 50e6);
        assertApproxEqAbs(IERC20(A_TOKEN).balanceOf(address(reserve)), 50e6, 2);
    }

    function testForkRealAaveFullWithdrawalReturnsExactUserPrincipal() public forkOnly {
        _deposit(1000e6);
        uint256 id = _requestAndApprove(1000e6);
        vm.prank(user);
        reserve.withdraw(id);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
        assertEq(reserve.totalPrincipal(), 0);
        assertFalse(reserve.isWithdrawalPending(user));
    }

    function testForkRealYieldAccrualAndTreasuryHarvestProtectsPrincipal() public forkOnly {
        _deposit(1000e6);
        vm.warp(block.timestamp + 30 days);
        uint256 available = reserve.availableYield();
        assertGt(available, 0);
        reserve.withdrawYield(available);
        assertEq(IERC20(USDC).balanceOf(treasury), available);
        assertEq(reserve.principalOf(user), 1000e6);
        assertGe(reserve.totalAssets(), 1000e6);
        uint256 id = _requestAndApprove(1000e6);
        vm.prank(user);
        reserve.withdraw(id);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
    }

    function testForkRealPoolLiquidityFailureRollsBackAndOriginalRequestRetries() public forkOnly {
        _deposit(50e6);
        uint256 id = _requestAndApprove(50e6);
        TokenSwoopReserve.WithdrawalRequest memory beforeRequest = reserve.getWithdrawal(user);
        uint256 poolLiquidity = IERC20(USDC).balanceOf(A_TOKEN);
        deal(USDC, A_TOKEN, 0); // LOCAL STORAGE ONLY, never modifies mainnet.
        vm.prank(user);
        vm.expectRevert();
        reserve.withdraw(id);
        assertEq(reserve.principalOf(user), 50e6);
        assertEq(keccak256(abi.encode(reserve.getWithdrawal(user))), keccak256(abi.encode(beforeRequest)));
        deal(USDC, A_TOKEN, poolLiquidity);
        vm.prank(user);
        reserve.withdraw(id);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
    }

    function testForkRealSupplyAllowanceFailureLeavesUserAndReserveUnchanged() public forkOnly {
        vm.prank(user);
        IERC20(USDC).approve(address(reserve), 49e6);
        vm.prank(user);
        vm.expectRevert();
        reserve.deposit(50e6);
        assertEq(reserve.totalPrincipal(), 0);
        assertEq(IERC20(USDC).balanceOf(user), 2000e6);
        assertEq(IERC20(USDC).allowance(user, address(reserve)), 49e6);
        assertEq(IERC20(A_TOKEN).balanceOf(address(reserve)), 0);
    }
}
