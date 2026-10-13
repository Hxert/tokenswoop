// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;
import {Test, Vm} from "forge-std/Test.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";

contract PaymentRecipientTest is Test {
    KUSDC token;
    TokenSwoopPayGBatch pay;
    address provider = address(0x111);
    address recipient = address(0x222);
    address user = address(0x333);
    function setUp() public {
        token = new KUSDC(address(this));
        pay = new TokenSwoopPayGBatch(address(token), address(this), provider, recipient);
        token.mint(user, 100e6);
        vm.prank(user); token.approve(address(pay), 100e6);
    }
    function testSingleAndBatchPayOnlyRecipientInOneTransfer() public {
        vm.recordLogs();
        vm.prank(provider); pay.charge(user, bytes32(uint256(1)), 1e6);
        vm.prank(provider); pay.chargeBatch(user, bytes32(uint256(2)), 2e6);
        assertEq(token.balanceOf(recipient), 3e6);
        assertEq(token.balanceOf(provider), 0);
        assertEq(token.balanceOf(address(pay)), 0);
        uint256 transfers;
        Vm.Log[] memory logs = vm.getRecordedLogs();
        for(uint256 i; i<logs.length; ++i) {
            if(logs[i].emitter==address(token) && logs[i].topics[0]==keccak256("Transfer(address,address,uint256)")) {
                ++transfers;
                assertEq(logs[i].topics[1], bytes32(uint256(uint160(user))));
                assertEq(logs[i].topics[2], bytes32(uint256(uint160(recipient))));
            }
        }
        assertEq(transfers, 2);
    }
    function testRecipientCannotChargeOrPause() public {
        vm.expectRevert(); vm.prank(recipient); pay.chargeBatch(user, bytes32(uint256(1)), 1e6);
        vm.expectRevert(); vm.prank(recipient); pay.pause();
    }
    function testBothEntryPointsConsumeTheSameBillAndDoNotDoublePay() public {
        bytes32 id=keccak256("bill");
        vm.prank(provider); pay.chargeBatch(user,id,1e6);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopPayG.RequestAlreadyCharged.selector,id));
        vm.prank(provider); pay.charge(user,id,1e6);
        assertEq(token.balanceOf(recipient),1e6);
    }
    function testCollectionFailureCanRetryAfterPauseAndAllowanceRecovery() public {
        vm.prank(user); token.approve(address(pay),0);
        vm.expectRevert(); vm.prank(provider); pay.chargeBatch(user,bytes32(uint256(1)),1e6);
        assertFalse(pay.isCharged(user,bytes32(uint256(1))));
        pay.pause();
        vm.prank(user); token.approve(address(pay),1e6);
        vm.expectRevert(); vm.prank(provider); pay.chargeBatch(user,bytes32(uint256(1)),1e6);
        pay.unpause();
        vm.prank(provider); pay.chargeBatch(user,bytes32(uint256(1)),1e6);
        assertEq(token.balanceOf(recipient),1e6);
    }
    function testZeroRecipientRejected() public {
        vm.expectRevert(TokenSwoopPayG.InvalidRecipient.selector);
        new TokenSwoopPayGBatch(address(token), address(this), provider, address(0));
    }
}
