// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {DeployReserveBase, DeployReserveTestnet, DeployReserveLocal} from "../script/DeployReserve.s.sol";
import {TokenSwoopReserve} from "../src/TokenSwoopReserve.sol";
import {TestnetAavePool} from "../src/testnet/TestnetAavePool.sol";
import {KUSDC} from "../src/KUSDC.sol";

contract ReserveDeploymentHarness is DeployReserveTestnet {
    function runWithConfig(address token) external returns (TestnetAavePool, TokenSwoopReserve) {
        return _deploy(address(0xD001), token, address(0xD003));
    }
}

contract ReserveDeploymentTest is Test {
    function testTestnetReserveScriptRejectsMainnetBeforeReadingEnvironment() public {
        DeployReserveTestnet script = new DeployReserveTestnet();
        vm.chainId(143);
        vm.expectRevert(abi.encodeWithSelector(DeployReserveBase.WrongChain.selector, 10143, 143));
        script.run();
    }

    function testLocalReserveScriptRejectsTestnet() public {
        DeployReserveLocal script = new DeployReserveLocal();
        vm.chainId(10143);
        vm.expectRevert(abi.encodeWithSelector(DeployReserveBase.WrongChain.selector, 31337, 10143));
        script.run();
    }

    function testReserveDeploymentSeedsPlatformBufferAndCorrectRoles() public {
        vm.chainId(10143);
        KUSDC token = new KUSDC(address(0xD001));
        ReserveDeploymentHarness script = new ReserveDeploymentHarness();
        (TestnetAavePool pool, TokenSwoopReserve reserve) = script.runWithConfig(address(token));
        assertEq(pool.owner(), address(0xD001));
        assertEq(reserve.owner(), address(0xD001));
        assertEq(reserve.settlementOperator(), address(0xD003));
        assertEq(reserve.treasury(), address(0xD001));
        assertEq(address(reserve.aavePool()), address(pool));
        assertEq(address(reserve.aToken()), address(pool.receiptToken()));
        assertEq(reserve.totalPrincipal(), 0);
        assertEq(reserve.totalAssets(), 1e6);
        assertEq(token.totalSupply(), 1e6);
        assertEq(token.balanceOf(address(0xD001)), 0);
    }

    function testDeploymentRejectsNonOwnerOfTestTokenBeforeBroadcast() public {
        vm.chainId(10143);
        KUSDC token = new KUSDC(address(this));
        ReserveDeploymentHarness script = new ReserveDeploymentHarness();
        vm.expectRevert(DeployReserveBase.NotTestTokenOwner.selector);
        script.runWithConfig(address(token));
        assertEq(token.totalSupply(), 0);
    }
}
