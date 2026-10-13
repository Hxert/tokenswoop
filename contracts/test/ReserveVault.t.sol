// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;
import {Test} from "forge-std/Test.sol";
import {TokenSwoopReserveVault} from "../src/TokenSwoopReserveVault.sol";
import {ReserveDebtTokenMock} from "./helpers/ReserveDebtMocks.sol";

contract ReserveVaultTest is Test {
    ReserveDebtTokenMock token;
    TokenSwoopReserveVault vault;
    address user=address(0x111);
    address manager=address(0x222);
    address operator=address(0x333);
    function setUp() public {
        token=new ReserveDebtTokenMock();
        vault=new TokenSwoopReserveVault(address(token),manager,operator);
        token.mint(user,1000e6);
        vm.prank(user); token.approve(address(vault),1000e6);
        vm.prank(user); vault.deposit(100e6);
    }
    function report(bytes32 id,uint256 amount,uint256 epoch) internal {
        bytes32[] memory ids=new bytes32[](1);ids[0]=id;
        uint256[] memory amounts=new uint256[](1);amounts[0]=amount;
        vm.prank(operator);vault.reportDebts(user,ids,amounts,epoch);
    }
    function testIdleCustodyAndFullWithdrawalWithoutBuffer() public {
        assertEq(token.balanceOf(address(vault)),100e6);
        assertEq(vault.totalAssets(),vault.totalPrincipal());
        assertEq(vault.custodyMode(),keccak256("vault"));
        vm.prank(user);vault.withdraw(100e6);
        assertEq(token.balanceOf(user),1000e6);
        assertEq(vault.totalAssets(),0);
    }
    function testDebtPartialPaymentAndRelease() public {
        report(bytes32(uint256(1)),10e6,0);
        vm.prank(user);vault.withdraw(90e6);
        vm.expectRevert();vm.prank(user);vault.withdraw(1);
        report(bytes32(uint256(1)),3e6,0);
        vm.prank(user);vault.withdraw(7e6);
        report(bytes32(uint256(1)),0,0);
        vm.prank(user);vault.withdraw(3e6);
        assertEq(vault.totalPrincipal(),0);
    }
    function testClearRejectsReplayedDebtAcrossEpochs() public {
        bytes32 id=bytes32(uint256(1));report(id,10e6,0);
        vm.prank(manager);vault.clearDebt(user);
        vm.expectRevert(TokenSwoopReserveVault.StaleDebtEpoch.selector);report(id,10e6,0);
        vm.expectRevert(abi.encodeWithSelector(TokenSwoopReserveVault.DebtReleased.selector,id));report(id,10e6,1);
        report(bytes32(uint256(2)),1e6,1);
        assertEq(vault.withdrawableOf(user),99e6);
    }
    function testDepositPauseLeavesWithdrawalAvailable() public {
        vm.prank(manager);vault.setDepositsPaused(true);
        vm.expectRevert();vm.prank(user);vault.deposit(1);
        vm.prank(user);vault.withdraw(100e6);
    }
    function testNeitherRoleCanSeizePrincipalOrWithdrawDonations() public {
        token.mint(address(vault),1e6);
        vm.expectRevert();vm.prank(manager);vault.withdraw(1);
        vm.expectRevert();vm.prank(operator);vault.withdraw(1);
        vm.prank(manager);(bool ok,)=address(vault).call(abi.encodeWithSignature("withdrawYield(uint256)",1));assertFalse(ok);
        vm.prank(manager);(ok,)=address(vault).call(abi.encodeWithSignature("approve(address,uint256)",operator,1));assertFalse(ok);
        assertEq(token.balanceOf(address(vault)),101e6);
    }
    function testManagerCannotReportAndOperatorCannotClearOrPause() public {
        bytes32[] memory ids=new bytes32[](1);ids[0]=bytes32(uint256(1));
        uint256[] memory amounts=new uint256[](1);amounts[0]=1;
        vm.expectRevert();vm.prank(manager);vault.reportDebts(user,ids,amounts,0);
        vm.expectRevert();vm.prank(operator);vault.clearDebt(user);
        vm.expectRevert();vm.prank(operator);vault.setDepositsPaused(true);
    }
    function testRejectUnsortedDuplicateEmptyOrMismatchedReports() public {
        bytes32[] memory ids=new bytes32[](2); ids[0]=bytes32(uint256(2));ids[1]=bytes32(uint256(1));
        uint256[] memory amounts=new uint256[](2);
        vm.expectRevert();vm.prank(operator);vault.reportDebts(user,ids,amounts,0);
        ids[1]=ids[0];vm.expectRevert();vm.prank(operator);vault.reportDebts(user,ids,amounts,0);
        ids[0]=bytes32(0);vm.expectRevert();vm.prank(operator);vault.reportDebts(user,ids,amounts,0);
        vm.expectRevert();vm.prank(operator);vault.reportDebts(user,new bytes32[](0),new uint256[](0),0);
        vm.expectRevert();vm.prank(operator);vault.reportDebts(user,ids,new uint256[](1),0);
    }
    function testTransferFailuresDoNotChangePrincipalOrConsumeAllowance() public {
        token.configure(true,false,address(0));
        vm.expectRevert();vm.prank(user);vault.deposit(1e6);
        assertEq(token.allowance(user,address(vault)),900e6);
        token.configure(false,true,address(0));
        vm.expectRevert();vm.prank(user);vault.deposit(1e6);
        assertEq(token.balanceOf(user),900e6);
        token.configure(false,false,user);
        vm.expectRevert();vm.prank(user);vault.withdraw(1e6);
        assertEq(vault.principalOf(user),100e6);
        assertEq(vault.totalPrincipal(),100e6);
    }
    function testReentryCannotWithdrawDuringDeposit() public {
        token.setCallback(address(vault),abi.encodeCall(vault.withdraw,(1)));
        vm.prank(user);vault.deposit(1e6);
        assertFalse(token.callbackSucceeded());
        assertEq(vault.totalPrincipal(),101e6);
    }
    function testFuzzSequenceMaintainsExactBacking(uint96 depositSeed,uint96 debtSeed,uint96 withdrawalSeed) public {
        uint256 amount=bound(uint256(depositSeed),1,900e6);
        vm.prank(user);vault.deposit(amount);
        uint256 debt=bound(uint256(debtSeed),0,2000e6);
        report(bytes32(uint256(1)),debt,0);
        uint256 available=vault.withdrawableOf(user);
        uint256 withdrawal=bound(uint256(withdrawalSeed),0,available);
        if(withdrawal>0){vm.prank(user);vault.withdraw(withdrawal);}
        assertEq(vault.totalAssets(),vault.totalPrincipal());
        assertEq(vault.principalOf(user)+token.balanceOf(user),1000e6);
        vm.prank(manager);vault.clearDebt(user);
        uint256 remaining=vault.principalOf(user);
        if(remaining>0){vm.prank(user);vault.withdraw(remaining);}
        assertEq(token.balanceOf(user),1000e6);
        assertEq(vault.totalAssets(),0);
    }
    function testInvalidRolesAndZeroAmountsRejected() public {
        vm.expectRevert();new TokenSwoopReserveVault(address(token),operator,operator);
        vm.expectRevert();new TokenSwoopReserveVault(address(token),address(0),operator);
        vm.expectRevert();new TokenSwoopReserveVault(address(token),manager,address(0));
        vm.expectRevert();new TokenSwoopReserveVault(address(0),manager,operator);
        vm.expectRevert();vm.prank(user);vault.deposit(0);
        vm.expectRevert();vm.prank(user);vault.withdraw(0);
    }
}
