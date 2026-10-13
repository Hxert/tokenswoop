// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {StdInvariant} from "forge-std/StdInvariant.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopReserve} from "../src/TokenSwoopReserve.sol";
import {TestnetAavePool} from "../src/testnet/TestnetAavePool.sol";

contract ReserveHandler is Test {
    KUSDC public immutable token;
    TestnetAavePool public immutable pool;
    TokenSwoopReserve public immutable reserve;
    address[3] public actors = [address(0x101), address(0x102), address(0x103)];
    mapping(address => uint256) public deposited;
    mapping(address => uint256) public withdrawn;
    uint256 public harvested;
    address internal constant TREASURY = address(0x303);

    struct Snapshot {
        uint256 principal;
        uint256 totalPrincipal;
        uint256 assets;
        uint256 wallet;
        uint256 allowance;
        uint256 receiptBalance;
        bytes32 requestHash;
    }

    constructor() {
        token = new KUSDC(address(this));
        pool = new TestnetAavePool(address(token), address(this));
        reserve = new TokenSwoopReserve(address(token), address(pool), address(this), address(0x201), TREASURY);
        token.mint(address(this), 1e6);
        token.approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
        for (uint256 i; i < actors.length; ++i) {
            token.mint(actors[i], 1_000_000e6);
        }
    }

    function deposit(uint256 actorSeed, uint256 amountSeed) external {
        address user = actors[actorSeed % actors.length];
        uint256 amount = bound(amountSeed, 2, 1000e6);
        if (token.balanceOf(user) < amount) token.mint(user, amount);
        vm.prank(user);
        token.approve(address(reserve), amount);
        Snapshot memory beforeState = _snapshot(user);
        vm.prank(user);
        (bool success,) = address(reserve).call(abi.encodeCall(reserve.deposit, (amount)));
        if (success) {
            deposited[user] += amount;
            assertEq(reserve.principalOf(user), beforeState.principal + amount);
            assertEq(token.balanceOf(user), beforeState.wallet - amount);
        } else {
            _assertUnchanged(user, beforeState);
        }
    }

    function request(uint256 actorSeed, uint256 amountSeed) external {
        address user = actors[actorSeed % actors.length];
        if (reserve.isWithdrawalPending(user) || reserve.principalOf(user) == 0) return;
        uint256 amount = bound(amountSeed, 1, reserve.principalOf(user));
        vm.prank(user);
        reserve.requestWithdrawal(amount);
    }

    function approve(uint256 actorSeed) external {
        address user = actors[actorSeed % actors.length];
        TokenSwoopReserve.WithdrawalRequest memory pending = reserve.getWithdrawal(user);
        if (!pending.active) return;
        address operator = reserve.settlementOperator();
        vm.prank(operator);
        reserve.approveWithdrawal(user, pending.requestId, uint64(block.timestamp + 1 hours));
    }

    function cancel(uint256 actorSeed) external {
        address user = actors[actorSeed % actors.length];
        if (!reserve.isWithdrawalPending(user)) return;
        vm.prank(user);
        reserve.cancelWithdrawal();
    }

    function clear(uint256 actorSeed) external {
        address user = actors[actorSeed % actors.length];
        TokenSwoopReserve.WithdrawalRequest memory pending = reserve.getWithdrawal(user);
        if (!pending.active) return;
        address operator = reserve.settlementOperator();
        vm.prank(operator);
        reserve.clearWithdrawalApproval(user, pending.requestId, keccak256("handler-clearance-revoked"));
    }

    function withdraw(uint256 actorSeed, bool staleId) external {
        address user = actors[actorSeed % actors.length];
        TokenSwoopReserve.WithdrawalRequest memory pending = reserve.getWithdrawal(user);
        uint256 id = pending.requestId + (staleId ? 1 : 0);
        Snapshot memory beforeState = _snapshot(user);
        vm.prank(user);
        (bool success,) = address(reserve).call(abi.encodeCall(reserve.withdraw, (id)));
        if (success) {
            assertFalse(staleId);
            assertTrue(pending.active);
            assertGt(pending.approvedUntil, block.timestamp);
            assertEq(pending.operatorVersion, reserve.operatorVersion());
            withdrawn[user] += pending.amount;
            assertEq(token.balanceOf(user), beforeState.wallet + pending.amount);
            assertEq(reserve.principalOf(user), beforeState.principal - pending.amount);
        } else {
            _assertUnchanged(user, beforeState);
        }
    }

    function fundYield(uint256 amountSeed) external {
        if (pool.receiptToken().scaledTotalSupply() == 0) return;
        uint256 amount = bound(amountSeed, 2, 10e6);
        token.mint(address(this), amount);
        token.approve(address(pool), amount);
        pool.accrueYield(amount);
    }

    function harvest(uint256 amountSeed) external {
        uint256 available = reserve.availableYield();
        if (available == 0) return;
        uint256 amount = bound(amountSeed, 1, available);
        uint256 principal = reserve.totalPrincipal();
        uint256 assets = reserve.totalAssets();
        uint256 treasuryBefore = token.balanceOf(TREASURY);
        (bool success,) = address(reserve).call(abi.encodeCall(reserve.withdrawYield, (amount)));
        if (success) {
            harvested += amount;
            assertEq(token.balanceOf(TREASURY), treasuryBefore + amount);
        } else {
            assertEq(reserve.totalAssets(), assets);
            assertEq(token.balanceOf(TREASURY), treasuryBefore);
        }
        assertEq(reserve.totalPrincipal(), principal);
    }

    function changeEnvironment(uint256 timeSeed, bool illiquid, bool paused, bool rotateOperator) external {
        vm.warp(block.timestamp + bound(timeSeed, 0, 2 hours));
        pool.setWithdrawalLimit(illiquid ? 0 : type(uint256).max);
        if (paused && !reserve.paused()) reserve.pause();
        if (!paused && reserve.paused()) reserve.unpause();
        if (rotateOperator) {
            reserve.setSettlementOperator(
                reserve.settlementOperator() == address(0x201) ? address(0x202) : address(0x201)
            );
        }
    }

    function _snapshot(address user) internal view returns (Snapshot memory s) {
        s.principal = reserve.principalOf(user);
        s.totalPrincipal = reserve.totalPrincipal();
        s.assets = reserve.totalAssets();
        s.wallet = token.balanceOf(user);
        s.allowance = token.allowance(user, address(reserve));
        s.receiptBalance = reserve.aToken().balanceOf(address(reserve));
        s.requestHash = keccak256(abi.encode(reserve.getWithdrawal(user)));
    }

    function _assertUnchanged(address user, Snapshot memory beforeState) internal view {
        assertEq(keccak256(abi.encode(_snapshot(user))), keccak256(abi.encode(beforeState)));
    }
}

contract ReserveInvariantTest is StdInvariant, Test {
    ReserveHandler internal handler;
    TokenSwoopReserve internal reserve;

    function setUp() public {
        handler = new ReserveHandler();
        reserve = handler.reserve();
        bytes4[] memory selectors = new bytes4[](9);
        selectors[0] = handler.deposit.selector;
        selectors[1] = handler.request.selector;
        selectors[2] = handler.approve.selector;
        selectors[3] = handler.cancel.selector;
        selectors[4] = handler.clear.selector;
        selectors[5] = handler.withdraw.selector;
        selectors[6] = handler.fundYield.selector;
        selectors[7] = handler.harvest.selector;
        selectors[8] = handler.changeEnvironment.selector;
        targetSelector(FuzzSelector(address(handler), selectors));
        targetContract(address(handler));
    }

    function invariant_PrincipalBackedAndMatchesDepositsLessApprovedWithdrawals() public view {
        uint256 sum;
        for (uint256 i; i < 3; ++i) {
            address user = handler.actors(i);
            uint256 principal = reserve.principalOf(user);
            assertEq(principal, handler.deposited(user) - handler.withdrawn(user));
            sum += principal;
        }
        assertEq(sum, reserve.totalPrincipal());
        assertGe(reserve.totalAssets(), sum);
        assertEq(handler.token().allowance(address(reserve), address(handler.pool())), 0);
    }
}
