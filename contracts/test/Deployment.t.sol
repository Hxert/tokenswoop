// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {DeployTestnet, DeployLocal, DeployBase} from "../script/DeployTestnet.s.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";

// Pass configuration directly so parallel tests never race on process-wide env vars.
contract DeploymentHarness is DeployTestnet {
    function runWithConfig(address token, address payg) external returns (KUSDC, TokenSwoopPayG, TokenSwoopPayGBatch) {
        return _deploy(address(0xD001), address(0xD002), address(0xD003), token, payg);
    }
}

contract LegacyPayG {
    address public immutable paymentToken;
    address public constant provider = address(0xD003);

    constructor(address token) {
        paymentToken = token;
    }

    function getAuthorization(address) external pure returns (uint256, uint256, uint256, uint64, bool, uint256) {
        return (0, 0, 0, 0, false, 0);
    }
}

contract DeploymentTest is Test {
    function testTestnetScriptRefusesMainnetBeforeReadingWallets() public {
        DeployTestnet script = new DeployTestnet();
        vm.chainId(143);
        vm.expectRevert(abi.encodeWithSelector(DeployBase.WrongChain.selector, 10143, 143));
        script.run();
    }

    function testLocalScriptRefusesTestnet() public {
        DeployLocal script = new DeployLocal();
        vm.chainId(10143);
        vm.expectRevert(abi.encodeWithSelector(DeployBase.WrongChain.selector, 31337, 10143));
        script.run();
    }

    function testDeploymentInitializesTokenAndOwnerAndSeedBalance() public {
        vm.chainId(10143);
        DeploymentHarness script = new DeploymentHarness();
        (KUSDC token, TokenSwoopPayG payg, TokenSwoopPayGBatch batch) = script.runWithConfig(address(0), address(0));
        assertEq(token.owner(), address(0xD001));
        assertEq(payg.owner(), address(0xD001));
        assertEq(batch.owner(), address(0xD001));
        assertEq(payg.provider(), address(0xD003));
        assertEq(batch.provider(), address(0xD003));
        assertEq(token.balanceOf(address(0xD002)), 100e6);
        assertEq(address(payg.paymentToken()), address(token));
        assertEq(address(batch.paymentToken()), address(token));
        assertTrue(address(payg) != address(batch));
    }

    function testDeploymentCanReuseTokenWithoutMintingOrChangingTokenOwner() public {
        vm.chainId(10143);
        KUSDC existing = new KUSDC(address(this));
        existing.mint(address(0xD002), 100e6);
        DeploymentHarness script = new DeploymentHarness();
        (KUSDC token, TokenSwoopPayG payg, TokenSwoopPayGBatch batch) =
            script.runWithConfig(address(existing), address(0));
        assertEq(address(token), address(existing));
        assertEq(token.owner(), address(this));
        assertEq(token.balanceOf(address(0xD002)), 100e6);
        assertEq(token.totalSupply(), 100e6);
        assertEq(payg.owner(), address(0xD001));
        assertEq(batch.owner(), address(0xD001));
        assertEq(address(batch.paymentToken()), address(existing));
    }

    function testDeploymentCanReuseTokenAndPayGAndOnlyCreateBatch() public {
        vm.chainId(10143);
        KUSDC existingToken = new KUSDC(address(this));
        existingToken.mint(address(0xD002), 100e6);
        TokenSwoopPayG existingPayG = new TokenSwoopPayG(address(existingToken), address(0xD001), address(0xD003), address(0xD003));
        DeploymentHarness script = new DeploymentHarness();
        (KUSDC token, TokenSwoopPayG payg, TokenSwoopPayGBatch batch) =
            script.runWithConfig(address(existingToken), address(existingPayG));
        assertEq(address(token), address(existingToken));
        assertEq(address(payg), address(existingPayG));
        assertEq(token.totalSupply(), 100e6);
        assertEq(token.balanceOf(address(0xD002)), 100e6);
        assertEq(token.owner(), address(this));
        assertEq(payg.owner(), address(0xD001));
        assertEq(batch.owner(), address(0xD001));
        assertEq(address(batch.paymentToken()), address(token));
        assertTrue(address(batch) != address(payg));
    }

    function testReusePayGRequiresMatchingFixedProvider() public {
        vm.chainId(10143);
        KUSDC token = new KUSDC(address(this));
        TokenSwoopPayG existing = new TokenSwoopPayG(address(token), address(this), address(0xBAD), address(0xBAD));
        DeploymentHarness script = new DeploymentHarness();
        vm.expectRevert(DeployBase.ExistingPayGProviderMismatch.selector);
        script.runWithConfig(address(token), address(existing));
    }

    function testReusePayGRequiresMatchingToken() public {
        vm.chainId(10143);
        KUSDC first = new KUSDC(address(this));
        KUSDC second = new KUSDC(address(this));
        TokenSwoopPayG existing = new TokenSwoopPayG(address(first), address(this), address(0xD003), address(0xD003));
        DeploymentHarness script = new DeploymentHarness();
        vm.expectRevert(DeployBase.ExistingPayGTokenMismatch.selector);
        script.runWithConfig(address(second), address(existing));
    }

    function testReuseRejectsLegacyBusinessAuthorizationABI() public {
        vm.chainId(10143);
        KUSDC token = new KUSDC(address(this));
        LegacyPayG legacy = new LegacyPayG(address(token));
        DeploymentHarness script = new DeploymentHarness();
        vm.expectRevert(DeployBase.ExistingPayGUnsupportedABI.selector);
        script.runWithConfig(address(token), address(legacy));
    }
}
