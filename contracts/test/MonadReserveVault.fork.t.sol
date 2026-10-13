// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;
import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {TokenSwoopReserveVault} from "../src/TokenSwoopReserveVault.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";
import {DeployReserveDebtMainnet} from "../script/DeployReserveDebt.s.sol";

interface ICircleVaultControls {
    function blacklister() external view returns(address);
    function pauser() external view returns(address);
    function blacklist(address) external;
    function unBlacklist(address) external;
    function pause() external;
    function unpause() external;
}

/// @dev Reads mainnet state; all writes and deals are in the local EVM only.
contract MonadReserveVaultForkTest is Test {
    address constant USDC=0x754704Bc059F8C67012fEd69BC8A327a5aafb603;
    address constant REVENUE=0x817e26Df18C0EBf2e1d0CA4A1c3C7dB87b9F78Fb;
    address user;address operator;
    TokenSwoopReserveVault vault;
    TokenSwoopPayGBatch pay;
    bool enabled;
    function setUp() public {
        enabled=vm.envOr("RUN_MAINNET_FORK",false);
        if(!enabled)return;
        vm.createSelectFork(vm.envString("MONAD_MAINNET_RPC_URL"),vm.envUint("MAINNET_FORK_BLOCK"));
        assertEq(block.chainid,143);
        user=makeAddr("vault-user");operator=makeAddr("vault-operator");
        vault=new TokenSwoopReserveVault(USDC,address(this),operator);
        pay=new TokenSwoopPayGBatch(USDC,address(this),operator,REVENUE);
        deal(USDC,user,100e6);
        vm.startPrank(user);
        IERC20(USDC).approve(address(vault),100e6);
        IERC20(USDC).approve(address(pay),10e6);
        vm.stopPrank();
    }
    modifier forkOnly(){if(!enabled){vm.skip(true);return;}_;}
    function deposit(uint256 amount) internal {vm.prank(user);vault.deposit(amount);}
    function report(bytes32 id,uint256 amount,uint256 epoch) internal {
        bytes32[] memory ids=new bytes32[](1);ids[0]=id;
        uint256[] memory amounts=new uint256[](1);amounts[0]=amount;
        vm.prank(operator);vault.reportDebts(user,ids,amounts,epoch);
    }
    function testForkIdleUSDCAndPaymentAllowanceIsUnchanged() public forkOnly {
        deposit(50e6);
        assertEq(vault.totalAssets(),50e6);
        assertEq(IERC20(USDC).balanceOf(address(vault)),50e6);
        assertEq(IERC20(USDC).allowance(user,address(pay)),10e6);
        assertEq(vault.custodyMode(),keccak256("vault"));
    }
    function testForkExactFullWithdrawalNeedsNoBufferOrApproval() public forkOnly {
        deposit(50e6);vm.prank(user);vault.withdraw(50e6);
        assertEq(IERC20(USDC).balanceOf(user),100e6);
        assertEq(vault.totalAssets(),0);
    }
    function testForkFailedBillFreezesOnlyDebtThenPaysRecipientExactlyOnce() public forkOnly {
        deposit(50e6);bytes32 id=keccak256("bill");
        vm.prank(user);IERC20(USDC).approve(address(pay),0);
        vm.expectRevert();vm.prank(operator);pay.chargeBatch(user,id,3e6);
        report(id,3e6,0);vm.prank(user);vault.withdraw(47e6);
        vm.expectRevert();vm.prank(user);vault.withdraw(1);
        vm.prank(user);IERC20(USDC).approve(address(pay),3e6);
        uint256 beforeBalance=IERC20(USDC).balanceOf(REVENUE);
        vm.prank(operator);pay.chargeBatch(user,id,3e6);
        vm.expectRevert();vm.prank(operator);pay.charge(user,id,3e6);
        assertEq(IERC20(USDC).balanceOf(REVENUE)-beforeBalance,3e6);
        assertEq(IERC20(USDC).balanceOf(operator),0);
        report(id,0,0);vm.prank(user);vault.withdraw(3e6);
        assertEq(IERC20(USDC).balanceOf(user),97e6);
    }
    function testForkManagerClearRejectsOldDebtReplay() public forkOnly {
        deposit(50e6);bytes32 id=bytes32(uint256(1));report(id,3e6,0);
        vault.clearDebt(user);
        vm.expectRevert();report(id,3e6,0);
        vm.expectRevert();report(id,3e6,1);
        vm.prank(user);vault.withdraw(50e6);
    }
    function testForkIndependentStopsPreserveWithdrawals() public forkOnly {
        deposit(50e6);pay.pause();vault.setDepositsPaused(true);
        vm.expectRevert();vm.prank(operator);pay.chargeBatch(user,bytes32(uint256(1)),1e6);
        vm.expectRevert();deposit(1e6);
        vm.prank(user);vault.withdraw(50e6);
        pay.unpause();vault.setDepositsPaused(false);deposit(1e6);
    }
    function testForkCirclePauseRollsBackPrincipalAndCanRecover() public forkOnly {
        deposit(50e6);ICircleVaultControls circle=ICircleVaultControls(USDC);
        address pauser=circle.pauser();vm.prank(pauser);circle.pause();
        vm.expectRevert();vm.prank(user);vault.withdraw(50e6);
        assertEq(vault.principalOf(user),50e6);
        vm.prank(pauser);circle.unpause();vm.prank(user);vault.withdraw(50e6);
        assertEq(IERC20(USDC).balanceOf(user),100e6);
    }
    function testForkRecipientBlacklistDoesNotConsumeBillAndRetryPaysOnce() public forkOnly {
        ICircleVaultControls circle=ICircleVaultControls(USDC);
        address blacklister=circle.blacklister();vm.prank(blacklister);circle.blacklist(REVENUE);
        bytes32 id=keccak256("blacklist");
        vm.expectRevert();vm.prank(operator);pay.chargeBatch(user,id,1e6);
        assertFalse(pay.isCharged(user,id));
        assertEq(IERC20(USDC).allowance(user,address(pay)),10e6);
        vm.prank(blacklister);circle.unBlacklist(REVENUE);
        vm.prank(operator);pay.chargeBatch(user,id,1e6);
        assertTrue(pay.isCharged(user,id));
    }
    function deploymentRoles() internal returns(address deployer) {
        deployer=makeAddr("zero-usdc-deployer");
        vm.setEnv("MAINNET_DEPLOYER_ADDRESS",vm.toString(deployer));
        vm.setEnv("MAINNET_PAYMENT_PROVIDER",vm.toString(operator));
        vm.setEnv("MAINNET_PAYMENT_RECIPIENT",vm.toString(REVENUE));
        vm.setEnv("MAINNET_PAYMENT_STOP_AUTHORITY",vm.toString(address(this)));
        vm.setEnv("MAINNET_RESERVE_MANAGER",vm.toString(address(this)));
        vm.setEnv("MAINNET_RESERVE_OPERATOR",vm.toString(operator));
        vm.deal(deployer,1 ether);
    }
    function testForkMainnetDeploymentWithoutUSDCUsesFixedRecipientAndIdleVault() public forkOnly {
        address deployer=deploymentRoles();assertEq(IERC20(USDC).balanceOf(deployer),0);
        (TokenSwoopPayGBatch p,TokenSwoopReserveVault v)=new DeployReserveDebtMainnet().run();
        assertEq(p.recipient(),REVENUE);assertEq(p.provider(),operator);assertEq(p.owner(),address(this));
        assertEq(v.manager(),address(this));assertEq(v.settlementOperator(),operator);
        assertEq(address(v.reserveToken()),USDC);assertEq(v.totalAssets(),0);assertEq(v.totalPrincipal(),0);
    }
    function testForkMainnetDeploymentRejectsTestnet() public forkOnly {
        deploymentRoles();DeployReserveDebtMainnet script=new DeployReserveDebtMainnet();vm.chainId(10143);
        vm.expectRevert(bytes("Monad Mainnet only"));script.run();
    }
    function testForkMainnetDeploymentRejectsProviderAsRevenueRecipient() public forkOnly {
        deploymentRoles();vm.setEnv("MAINNET_PAYMENT_RECIPIENT",vm.toString(operator));
        DeployReserveDebtMainnet script=new DeployReserveDebtMainnet();
        vm.expectRevert(bytes("Separate revenue recipient required"));script.run();
    }
}
