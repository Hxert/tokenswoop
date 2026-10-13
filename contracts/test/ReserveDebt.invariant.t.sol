// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopReserveDebt} from "../src/TokenSwoopReserveDebt.sol";
import {TestnetAavePool} from "../src/testnet/TestnetAavePool.sol";

/// @dev Reference ledger changes only after a successful, explicitly selected
/// action. Every onchain request entry is checked against this independent ledger.
contract ReserveDebtHandler is Test {
    struct ExpectedDebt {
        uint256 amount;
        uint256 epoch;
    }

    uint256 public constant INITIAL_BALANCE = 1_000_000e6;
    address public constant MANAGER = address(0xab11);
    address public constant OPERATOR = address(0xab22);
    address public constant TREASURY = address(0xab33);
    address[4] public actors = [address(0xac01), address(0xac02), address(0xac03), address(0xac04)];
    KUSDC public immutable token;
    TestnetAavePool public immutable pool;
    TokenSwoopReserveDebt public immutable reserve;
    mapping(address => uint256) public deposited;
    mapping(address => uint256) public withdrawn;
    mapping(address => uint256) public epochs;
    mapping(address => bytes32[]) internal keys;
    mapping(address => mapping(bytes32 => ExpectedDebt)) internal debts;
    uint256 internal nextID = 1;
    uint256 public bufferFunded;
    uint256 public yieldFunded;
    uint256 public harvested;
    bool public depositsPaused;

    constructor() {
        token = new KUSDC(address(this));
        pool = new TestnetAavePool(address(token), address(this));
        reserve = new TokenSwoopReserveDebt(address(token), address(pool), MANAGER, OPERATOR, TREASURY);
        token.mint(address(this), INITIAL_BALANCE);
        token.approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
        bufferFunded = 1e6;
        for (uint256 i; i < actors.length; ++i) {
            address user = actors[i];
            token.mint(user, INITIAL_BALANCE);
            vm.startPrank(user);
            token.approve(address(reserve), type(uint256).max);
            reserve.deposit(100e6);
            vm.stopPrank();
            deposited[user] = 100e6;
            // Reuse the same ID across different users to exercise namespacing.
            _sendReport(user, bytes32(uint256(1)), 15e6, 0);
            keys[user].push(bytes32(uint256(1)));
            debts[user][bytes32(uint256(1))] = ExpectedDebt(15e6, 0);
        }
    }

    function deposit(uint256 actorSeed, uint256 amountSeed) external {
        address user = actors[actorSeed % actors.length];
        uint256 amount = bound(amountSeed, 1, 1000e6);
        if (token.balanceOf(user) < amount) return;
        vm.prank(user);
        (bool ok, bytes memory reason) = address(reserve).call(abi.encodeCall(reserve.deposit, (amount)));
        if (ok) {
            assertFalse(depositsPaused);
            assertFalse(pool.paused());
            deposited[user] += amount;
        } else {
            bytes4 error = _selector(reason);
            if (depositsPaused) assertEq(error, TokenSwoopReserveDebt.DepositsPaused.selector);
            else if (pool.paused()) assertEq(error, Pausable.EnforcedPause.selector);
            else if (amount * 1e27 / pool.liquidityIndex() == 0) assertEq(error, TestnetAavePool.InvalidAmount.selector);
            else assertEq(error, TokenSwoopReserveDebt.InsufficientBacking.selector);
        }
    }

    function withdraw(uint256 actorSeed, uint256 amountSeed) external {
        address user = actors[actorSeed % actors.length];
        uint256 principal = deposited[user] - withdrawn[user];
        uint256 debt = _debtTotal(user);
        uint256 available = principal > debt ? principal - debt : 0;
        uint256 amount = amountSeed % 4 == 0 ? available : bound(amountSeed, 1, principal + 1e6);
        uint256 idle = token.balanceOf(address(reserve));
        vm.prank(user);
        (bool ok, bytes memory reason) = address(reserve).call(abi.encodeCall(reserve.withdraw, (amount)));
        if (ok) {
            assertGt(amount, 0);
            assertLe(amount, available, "frozen principal escaped");
            withdrawn[user] += amount;
        } else {
            bytes4 error = _selector(reason);
            if (amount == 0) assertEq(error, TokenSwoopReserveDebt.ZeroAmount.selector);
            else if (amount > available) assertEq(error, TokenSwoopReserveDebt.InsufficientWithdrawable.selector);
            else _assertRedemptionFailure(error, amount, idle);
        }
    }

    function report(uint256 actorSeed, uint256 keySeed, uint256 amountSeed, bool fresh) external {
        address user = actors[actorSeed % actors.length];
        bytes32 id = fresh ? bytes32(++nextID) : keys[user][keySeed % keys[user].length];
        uint256 amount = bound(amountSeed, 0, 500e6);
        if (!fresh && debts[user][id].epoch != epochs[user]) {
            vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserveDebt.DebtReleased.selector, id));
            _sendReport(user, id, amount, epochs[user]);
            return;
        }
        _sendReport(user, id, amount, epochs[user]);
        if (fresh) keys[user].push(id);
        debts[user][id] = ExpectedDebt(amount, epochs[user]);
    }

    function partialPayment(uint256 actorSeed, uint256 keySeed, uint256 remainingSeed) external {
        address user = actors[actorSeed % actors.length];
        bytes32 id = keys[user][keySeed % keys[user].length];
        ExpectedDebt storage expected = debts[user][id];
        if (expected.epoch != epochs[user]) return;
        uint256 remaining = bound(remainingSeed, 0, expected.amount);
        _sendReport(user, id, remaining, epochs[user]);
        _sendReport(user, id, remaining, epochs[user]);
        expected.amount = remaining;
    }

    function clearDebt(uint256 actorSeed) external {
        address user = actors[actorSeed % actors.length];
        vm.prank(MANAGER);
        reserve.clearDebt(user);
        ++epochs[user];
    }

    function staleReport(uint256 actorSeed, uint256 keySeed) external {
        address user = actors[actorSeed % actors.length];
        uint256 wrongEpoch = epochs[user] == 0 ? 1 : epochs[user] - 1;
        vm.expectRevert(TokenSwoopReserveDebt.StaleDebtEpoch.selector);
        _sendReport(user, keys[user][keySeed % keys[user].length], 20e6, wrongEpoch);
    }

    function invalidBatch(uint256 actorSeed) external {
        address user = actors[actorSeed % actors.length];
        bytes32[] memory ids = new bytes32[](2);
        uint256[] memory amounts = new uint256[](2);
        ids[0] = ids[1] = bytes32(nextID + 1);
        amounts[0] = 1e6;
        amounts[1] = 2e6;
        vm.expectRevert(TokenSwoopReserveDebt.InvalidDebtReport.selector);
        vm.prank(OPERATOR);
        reserve.reportDebts(user, ids, amounts, epochs[user]);
        (uint256 amount, uint256 epoch, bool reported) = reserve.debts(user, ids[0]);
        assertEq(amount, 0);
        assertEq(epoch, 0);
        assertFalse(reported);
    }

    function unauthorized(uint256 actorSeed, uint8 operation) external {
        address user = actors[actorSeed % actors.length];
        if (operation % 4 == 0) {
            vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
            vm.prank(user);
            reserve.clearDebt(actors[(actorSeed % actors.length + 1) % actors.length]);
        } else if (operation % 4 == 1) {
            vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
            vm.prank(OPERATOR);
            reserve.setDepositsPaused(!depositsPaused);
        } else if (operation % 4 == 2) {
            vm.expectRevert(TokenSwoopReserveDebt.NotManager.selector);
            vm.prank(user);
            reserve.withdrawYield(1);
        } else {
            bytes32[] memory ids = new bytes32[](1);
            ids[0] = bytes32(nextID + 1);
            uint256[] memory amounts = new uint256[](1);
            amounts[0] = 1;
            vm.expectRevert(TokenSwoopReserveDebt.NotOperator.selector);
            vm.prank(MANAGER);
            reserve.reportDebts(user, ids, amounts, epochs[user]);
        }
    }

    function changeEnvironment(bool stopDeposits, bool illiquid, bool stopPool) external {
        vm.prank(MANAGER);
        reserve.setDepositsPaused(stopDeposits);
        depositsPaused = stopDeposits;
        pool.setWithdrawalLimit(illiquid ? 0 : type(uint256).max);
        if (stopPool && !pool.paused()) pool.pause();
        else if (!stopPool && pool.paused()) pool.unpause();
    }

    function fundYield(uint256 amountSeed) external {
        if (pool.paused() || pool.receiptToken().scaledTotalSupply() == 0) return;
        uint256 amount = bound(amountSeed, 1, 10e6);
        token.approve(address(pool), amount);
        pool.accrueYield(amount);
        yieldFunded += amount;
    }

    function fundBuffer(uint256 amountSeed) external {
        uint256 amount = bound(amountSeed, 1, 1e6);
        token.approve(address(reserve), amount);
        reserve.fundBuffer(amount);
        bufferFunded += amount;
    }

    function harvest(uint256 amountSeed) external {
        uint256 assets = reserve.totalAssets();
        uint256 principal = reserve.totalPrincipal();
        uint256 available = assets > principal + 1e6 ? assets - principal - 1e6 : 0;
        uint256 amount = bound(amountSeed, 1, available + 1);
        uint256 idle = token.balanceOf(address(reserve));
        vm.prank(MANAGER);
        (bool ok, bytes memory reason) = address(reserve).call(abi.encodeCall(reserve.withdrawYield, (amount)));
        if (ok) {
            assertLe(amount, available, "treasury took principal or protected buffer");
            harvested += amount;
        } else if (amount > available) {
            assertEq(_selector(reason), TokenSwoopReserveDebt.ExcessYieldWithdrawal.selector);
        } else {
            _assertRedemptionFailure(_selector(reason), amount, idle);
        }
    }

    function _assertRedemptionFailure(bytes4 error, uint256 amount, uint256 idle) internal view {
        if (amount > idle && pool.paused()) {
            assertEq(error, Pausable.EnforcedPause.selector);
        } else if (amount > idle && amount - idle > pool.withdrawalLimit()) {
            assertEq(error, TestnetAavePool.InsufficientLiquidity.selector);
        } else {
            assertEq(error, TokenSwoopReserveDebt.InsufficientBacking.selector);
        }
    }

    function _sendReport(address user, bytes32 id, uint256 amount, uint256 epoch) internal {
        bytes32[] memory ids = new bytes32[](1);
        ids[0] = id;
        uint256[] memory amounts = new uint256[](1);
        amounts[0] = amount;
        vm.prank(OPERATOR);
        reserve.reportDebts(user, ids, amounts, epoch);
    }

    function _selector(bytes memory reason) internal pure returns (bytes4) {
        require(reason.length >= 4, "unexpected empty revert");
        return bytes4(reason);
    }

    function _debtTotal(address user) internal view returns (uint256 total) {
        for (uint256 j; j < keys[user].length; ++j) {
            ExpectedDebt storage expected = debts[user][keys[user][j]];
            if (expected.epoch == epochs[user]) total += expected.amount;
        }
    }

    function assertModel() public view {
        uint256 principalSum;
        uint256 balances = token.balanceOf(address(this)) + token.balanceOf(TREASURY)
            + token.balanceOf(address(reserve)) + token.balanceOf(address(pool.receiptToken()));
        for (uint256 i; i < actors.length; ++i) {
            address user = actors[i];
            uint256 principal = deposited[user] - withdrawn[user];
            uint256 debt = _debtTotal(user);
            uint256 frozen = debt < principal ? debt : principal;
            principalSum += principal;
            balances += token.balanceOf(user);
            assertEq(reserve.principalOf(user), principal, "principal differs from actual deposits less withdrawals");
            assertEq(reserve.debtOf(user), debt, "debt differs from current-epoch request entries");
            assertEq(reserve.debtEpoch(user), epochs[user]);
            assertEq(reserve.frozenOf(user), frozen);
            assertEq(reserve.withdrawableOf(user), principal - frozen);
            assertEq(token.balanceOf(user) + principal, INITIAL_BALANCE, "user principal was diverted");
            assertEq(token.allowance(user, address(reserve)), type(uint256).max - deposited[user]);
            for (uint256 j; j < keys[user].length; ++j) {
                bytes32 id = keys[user][j];
                (uint256 amount, uint256 epoch, bool reported) = reserve.debts(user, id);
                ExpectedDebt storage expected = debts[user][id];
                assertEq(amount, expected.amount);
                assertEq(epoch, expected.epoch);
                assertTrue(reported);
            }
        }
        assertEq(reserve.totalPrincipal(), principalSum);
        assertGe(reserve.totalAssets(), principalSum, "principal became unbacked");
        assertEq(token.allowance(address(reserve), address(pool)), 0, "pool allowance left behind");
        assertEq(token.balanceOf(address(this)), INITIAL_BALANCE - bufferFunded - yieldFunded);
        assertEq(token.balanceOf(TREASURY), harvested);
        assertEq(token.balanceOf(MANAGER), 0);
        assertEq(token.balanceOf(OPERATOR), 0);
        assertEq(balances, token.totalSupply(), "tokens escaped the tracked holders");
        assertEq(token.totalSupply(), INITIAL_BALANCE * (actors.length + 1));
        assertEq(reserve.paused(), depositsPaused);
        assertEq(reserve.manager(), MANAGER);
        assertEq(reserve.settlementOperator(), OPERATOR);
    }

    // Not a fuzz target. Every completed sequence must still let all users exit
    // once the manager releases debt and the simulated pool restores liquidity.
    function exitAll() external {
        if (pool.paused()) pool.unpause();
        pool.setWithdrawalLimit(type(uint256).max);
        // Keep the reserve's deposit pause unchanged: it must never block exit.
        for (uint256 i; i < actors.length; ++i) {
            address user = actors[i];
            vm.prank(MANAGER);
            reserve.clearDebt(user);
            ++epochs[user];
            uint256 principal = deposited[user] - withdrawn[user];
            if (principal > 0) {
                vm.prank(user);
                reserve.withdraw(principal);
                withdrawn[user] += principal;
            }
            assertEq(token.balanceOf(user), INITIAL_BALANCE, "full exit did not return principal");
        }
        assertModel();
        assertEq(reserve.totalPrincipal(), 0);
    }
}

contract ReserveDebtInvariantTest is Test {
    ReserveDebtHandler internal handler;

    function setUp() public {
        handler = new ReserveDebtHandler();
        bytes4[] memory selectors = new bytes4[](12);
        selectors[0] = handler.deposit.selector;
        selectors[1] = handler.withdraw.selector;
        selectors[2] = handler.report.selector;
        selectors[3] = handler.partialPayment.selector;
        selectors[4] = handler.clearDebt.selector;
        selectors[5] = handler.staleReport.selector;
        selectors[6] = handler.invalidBatch.selector;
        selectors[7] = handler.unauthorized.selector;
        selectors[8] = handler.changeEnvironment.selector;
        selectors[9] = handler.fundYield.selector;
        selectors[10] = handler.fundBuffer.selector;
        selectors[11] = handler.harvest.selector;
        targetSelector(FuzzSelector(address(handler), selectors));
        targetContract(address(handler));
    }

    function invariant_PrincipalDebtEpochsPermissionsAndTokenConservation() public view {
        handler.assertModel();
    }

    function afterInvariant() public {
        handler.exitAll();
    }
}
