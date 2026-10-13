// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";

contract PayGHandler is Test {
    struct ChargeContext {
        address user;
        address caller;
        bytes32 billId;
        bool paid;
        uint256 userBalance;
        uint256 providerBalance;
        uint256 allowance;
        uint256 amount;
    }

    KUSDC public immutable token;
    TokenSwoopPayG public immutable payg;
    address public immutable admin;
    address[3] public users;
    address[2] public callers;
    address public constant SINK = address(0xDEAD);
    mapping(address => uint256) public expectedAllowance;
    mapping(address => uint256) public totalPaid;
    mapping(address => uint256) public totalDrained;
    mapping(address => mapping(bytes32 => uint256)) public billUses;
    uint256 public successfulCharges;
    uint256 public failedCharges;

    constructor(KUSDC token_, TokenSwoopPayG payg_, address admin_) {
        token = token_;
        payg = payg_;
        admin = admin_;
        users = [address(0x1001), address(0x1002), address(0x1003)];
        callers = [payg.provider(), address(0x2002)];
    }

    function approve(uint8 userIndex, uint96 rawAmount) external {
        _approve(users[userIndex % 3], bound(uint256(rawAmount), 0, 200e6));
    }

    function revoke(uint8 userIndex) external {
        _approve(users[userIndex % 3], 0);
    }

    function _approve(address user, uint256 amount) internal {
        vm.prank(user);
        try token.approve(address(payg), amount) {
            expectedAllowance[user] = amount;
        } catch {}
    }

    function drain(uint8 userIndex, uint96 rawAmount) external {
        address user = users[userIndex % 3];
        if (token.paused() || token.isBlacklisted(user)) return;
        uint256 amount = bound(uint256(rawAmount), 0, token.balanceOf(user));
        vm.prank(user);
        token.transfer(SINK, amount);
        totalDrained[user] += amount;
    }

    function setRestrictions(uint8 userIndex, bool paymentPaused, bool tokenPaused, bool blacklisted) external {
        vm.startPrank(admin);
        if (paymentPaused != payg.paused()) {
            if (paymentPaused) payg.pause();
            else payg.unpause();
        }
        if (tokenPaused != token.paused()) {
            if (tokenPaused) token.pause();
            else token.unpause();
        }
        address user = users[userIndex % 3];
        if (blacklisted != token.isBlacklisted(user)) {
            if (blacklisted) token.blacklist(user);
            else token.unBlacklist(user);
        }
        vm.stopPrank();
    }

    function charge(uint8 userIndex, uint8 callerIndex, uint8 billSlot, uint96 rawAmount) external {
        _charge(userIndex, callerIndex, billSlot, rawAmount, false);
    }

    function chargeBatch(uint8 userIndex, uint8 callerIndex, uint8 billSlot, uint96 rawAmount) external {
        _charge(userIndex, callerIndex, billSlot, rawAmount, true);
    }

    function _charge(uint8 userIndex, uint8 callerIndex, uint8 idSlot, uint96 rawAmount, bool batch) internal {
        ChargeContext memory ctx;
        ctx.user = users[userIndex % 3];
        ctx.caller = callers[callerIndex % 2];
        ctx.billId = bytes32(uint256(idSlot % 16));
        ctx.amount = bound(uint256(rawAmount), 0, 10e6);
        ctx.paid = payg.isCharged(ctx.user, ctx.billId);
        ctx.userBalance = token.balanceOf(ctx.user);
        ctx.providerBalance = token.balanceOf(payg.provider());
        ctx.allowance = token.allowance(ctx.user, address(payg));
        vm.prank(ctx.caller);
        (bool success, bytes memory result) = address(payg)
            .call(
                batch
                    ? abi.encodeCall(TokenSwoopPayGBatch.chargeBatch, (ctx.user, ctx.billId, ctx.amount))
                    : abi.encodeCall(TokenSwoopPayG.charge, (ctx.user, ctx.billId, ctx.amount))
            );
        if (success) {
            ++successfulCharges;
            assertEq(ctx.caller, payg.provider(), "unauthorized charge succeeded");
            assertGt(ctx.amount, 0, "zero payment succeeded");
            assertFalse(ctx.paid, "already-paid ID succeeded");
            assertLe(ctx.amount, ctx.allowance, "payment exceeded allowance");
            assertLe(ctx.amount, ctx.userBalance, "payment exceeded balance");
            if (batch) assertEq(abi.decode(result, (uint256)), ctx.amount);
            assertEq(++billUses[ctx.user][ctx.billId], 1, "duplicate bill payment");
            totalPaid[ctx.user] += ctx.amount;
            expectedAllowance[ctx.user] -= ctx.amount;
            assertEq(token.balanceOf(ctx.user), ctx.userBalance - ctx.amount);
            assertEq(token.balanceOf(payg.provider()), ctx.providerBalance + ctx.amount);
            assertEq(token.allowance(ctx.user, address(payg)), ctx.allowance - ctx.amount);
            assertTrue(payg.isCharged(ctx.user, ctx.billId));
        } else {
            ++failedCharges;
            assertEq(payg.isCharged(ctx.user, ctx.billId), ctx.paid, "failed payment consumed ID");
            assertEq(token.balanceOf(ctx.user), ctx.userBalance, "failed payment debited user");
            assertEq(token.balanceOf(payg.provider()), ctx.providerBalance, "failed payment credited provider");
            assertEq(token.allowance(ctx.user, address(payg)), ctx.allowance, "failed payment consumed allowance");
        }
    }
}

contract PayGInvariantTest is Test {
    KUSDC internal token;
    TokenSwoopPayG internal payg;
    PayGHandler internal handler;

    function _newPaymentContract(KUSDC token_) internal virtual returns (TokenSwoopPayG) {
        return new TokenSwoopPayG(address(token_), address(this), address(0x2001), address(0x2001));
    }

    function _isBatch() internal pure virtual returns (bool) {
        return false;
    }

    function setUp() public {
        token = new KUSDC(address(this));
        payg = _newPaymentContract(token);
        handler = new PayGHandler(token, payg, address(this));
        for (uint8 i; i < 3; ++i) {
            token.mint(handler.users(i), 100e6);
            handler.approve(i, 10e6);
        }
        bytes4[] memory selectors = new bytes4[](_isBatch() ? 6 : 5);
        selectors[0] = PayGHandler.approve.selector;
        selectors[1] = PayGHandler.revoke.selector;
        selectors[2] = PayGHandler.charge.selector;
        selectors[3] = PayGHandler.drain.selector;
        selectors[4] = PayGHandler.setRestrictions.selector;
        if (_isBatch()) selectors[5] = PayGHandler.chargeBatch.selector;
        targetSelector(FuzzSelector({addr: address(handler), selectors: selectors}));
        targetContract(address(handler));
    }

    function _assertAllowanceAccounting() internal view {
        uint256 totalPaid;
        for (uint256 u; u < 3; ++u) {
            address user = handler.users(u);
            assertEq(token.allowance(user, address(payg)), handler.expectedAllowance(user));
            assertEq(token.balanceOf(user) + handler.totalPaid(user) + handler.totalDrained(user), 100e6);
            totalPaid += handler.totalPaid(user);
        }
        assertEq(token.balanceOf(payg.provider()), totalPaid);
    }

    function _assertPermanentDeduplication() internal view {
        for (uint256 u; u < 3; ++u) {
            for (uint256 r; r < 16; ++r) {
                uint256 uses = handler.billUses(handler.users(u), bytes32(r));
                assertLe(uses, 1);
                assertEq(payg.isCharged(handler.users(u), bytes32(r)), uses == 1);
            }
        }
    }

    function invariant_fundsAreConservedAndPaymentContractNeverCustodies() public view {
        _assertAllowanceAccounting();
        _assertPermanentDeduplication();
        assertEq(token.balanceOf(handler.callers(1)), 0);
        uint256 total = token.balanceOf(handler.SINK()) + token.balanceOf(payg.provider());
        for (uint256 i; i < 3; ++i) {
            total += token.balanceOf(handler.users(i));
        }
        assertEq(total, 300e6);
        assertEq(token.totalSupply(), 300e6);
        assertEq(token.balanceOf(address(payg)), 0);
    }
}

contract PayGBatchInvariantTest is PayGInvariantTest {
    function _newPaymentContract(KUSDC token_) internal override returns (TokenSwoopPayG) {
        return new TokenSwoopPayGBatch(address(token_), address(this), address(0x2001), address(0x2001));
    }

    function _isBatch() internal pure override returns (bool) {
        return true;
    }
}
