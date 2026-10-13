// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test, Vm} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {IERC20Metadata} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Metadata.sol";
import {IERC20Permit} from "@openzeppelin/contracts/token/ERC20/extensions/IERC20Permit.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";

/// @dev RPC reads only. All deals, approvals and payments occur on the local fork.
contract MonadUSDCForkTest is Test {
    address internal constant USDC = 0x754704Bc059F8C67012fEd69BC8A327a5aafb603;
    uint256 internal constant USER_KEY = 0xA11CE;
    address internal user;
    address internal provider;
    address internal recipient;
    TokenSwoopPayG internal payg;
    TokenSwoopPayGBatch internal batch;
    bool internal enabled;

    function setUp() public {
        enabled = vm.envOr("RUN_MAINNET_FORK", false);
        if (!enabled) return;
        string memory rpc = vm.envOr("MONAD_MAINNET_RPC_URL", string("https://rpc.monad.xyz"));
        uint256 pinnedBlock = vm.envOr("MAINNET_FORK_BLOCK", uint256(0));
        if (pinnedBlock == 0) vm.createSelectFork(rpc);
        else vm.createSelectFork(rpc, pinnedBlock);
        assertEq(block.chainid, 143);
        user = vm.addr(USER_KEY);
        provider = makeAddr("tokenswoop-provider");
        recipient = makeAddr("independent-revenue-wallet");
        payg = new TokenSwoopPayG(USDC, address(this), provider, recipient);
        batch = new TokenSwoopPayGBatch(USDC, address(this), provider, recipient);
        deal(USDC, user, 100e6);
        vm.startPrank(user);
        IERC20(USDC).approve(address(payg), 10e6);
        IERC20(USDC).approve(address(batch), 10e6);
        vm.stopPrank();
    }

    function testForkRealUSDCBillCharge() public {
        if (!enabled) {
            vm.skip(true);
            return;
        }
        assertEq(IERC20Metadata(USDC).decimals(), 6);
        vm.prank(provider);
        payg.charge(user, keccak256("mainnet-fork-only"), 4200);
        assertEq(IERC20(USDC).balanceOf(user), 100e6 - 4200);
        assertEq(IERC20(USDC).balanceOf(recipient), 4200);
        assertEq(IERC20(USDC).allowance(user, address(payg)), 10e6 - 4200);
    }

    function testForkRealUSDCMaximumAllowanceDecrements() public {
        if (!enabled) {
            vm.skip(true);
            return;
        }
        vm.prank(user);
        IERC20(USDC).approve(address(payg), type(uint256).max);
        vm.prank(provider);
        payg.charge(user, keccak256("max-allowance"), 4200);
        assertEq(IERC20(USDC).allowance(user, address(payg)), type(uint256).max - 4200);
    }

    function testForkRealUSDCPermitFundsPaymentAllowance() public {
        if (!enabled) {
            vm.skip(true);
            return;
        }
        IERC20Permit permitToken = IERC20Permit(USDC);
        uint256 deadline = block.timestamp + 1 days;
        bytes32 permitHash = keccak256(
            abi.encode(
                keccak256("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)"),
                user,
                address(payg),
                1e6,
                permitToken.nonces(user),
                deadline
            )
        );
        (uint8 v, bytes32 r, bytes32 s) =
            vm.sign(USER_KEY, keccak256(abi.encodePacked(hex"1901", permitToken.DOMAIN_SEPARATOR(), permitHash)));
        permitToken.permit(user, address(payg), 1e6, deadline, v, r, s);
        vm.prank(provider);
        payg.charge(user, keccak256("permit-payment"), 4200);
        assertEq(IERC20(USDC).allowance(user, address(payg)), 1e6 - 4200);
    }

    function testForkRealUSDCBillTotalUsesOneTransferAndOneBillReceipt() public {
        if (!enabled) {
            vm.skip(true);
            return;
        }
        bytes32 billId = keccak256("fork-bill");
        vm.recordLogs();
        vm.prank(provider);
        assertEq(batch.chargeBatch(user, billId, 12600), 12600);
        Vm.Log[] memory logs = vm.getRecordedLogs();
        uint256 transfers;
        uint256 billReceipts;
        for (uint256 i; i < logs.length; ++i) {
            if (logs[i].emitter == USDC && logs[i].topics[0] == keccak256("Transfer(address,address,uint256)")) {
                ++transfers;
                assertEq(logs[i].topics[2], bytes32(uint256(uint160(recipient))));
                assertEq(abi.decode(logs[i].data, (uint256)), 12600);
            }
            if (
                logs[i].emitter == address(batch)
                    && logs[i].topics[0] == keccak256("BatchCharged(address,address,bytes32,uint256)")
            ) {
                ++billReceipts;
                assertEq(logs[i].topics[3], billId);
                uint256 total = abi.decode(logs[i].data, (uint256));
                assertEq(total, 12600);
            }
        }
        assertEq(transfers, 1);
        assertEq(billReceipts, 1);
        assertTrue(batch.isCharged(user, billId));
        assertEq(IERC20(USDC).balanceOf(user), 100e6 - 12600);
        assertEq(IERC20(USDC).balanceOf(recipient), 12600);
        assertEq(IERC20(USDC).allowance(user, address(batch)), 10e6 - 12600);
    }

    function testForkRealUSDCFailedBillRollsBackAndRetriesSameBillId() public {
        if (!enabled) {
            vm.skip(true);
            return;
        }
        bytes32 billId = keccak256("fork-bill");
        vm.prank(user);
        IERC20(USDC).approve(address(batch), 8400);
        vm.prank(provider);
        vm.expectRevert();
        batch.chargeBatch(user, billId, 12600);
        assertFalse(batch.isCharged(user, billId));
        assertEq(IERC20(USDC).balanceOf(user), 100e6);
        assertEq(IERC20(USDC).allowance(user, address(batch)), 8400);
        vm.prank(user);
        IERC20(USDC).approve(address(batch), 12600);
        vm.prank(provider);
        batch.chargeBatch(user, billId, 12600);
        assertTrue(batch.isCharged(user, billId));
    }

    function testForkRealUSDCBillMaximumAllowanceDecrements() public {
        if (!enabled) {
            vm.skip(true);
            return;
        }
        vm.prank(user);
        IERC20(USDC).approve(address(batch), type(uint256).max);
        vm.prank(provider);
        batch.chargeBatch(user, keccak256("fork-bill"), 12600);
        assertEq(IERC20(USDC).allowance(user, address(batch)), type(uint256).max - 12600);
    }
}
