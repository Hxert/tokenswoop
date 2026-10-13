// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Script} from "forge-std/Script.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopPayG} from "../src/TokenSwoopPayG.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";

abstract contract DeployBase is Script {
    error WrongChain(uint256 expected, uint256 actual);
    error InvalidDemoWallets();
    error ExistingPayGTokenMismatch();
    error ExistingPayGProviderMismatch();
    error ExistingPayGUnsupportedABI();

    function expectedChainId() public pure virtual returns (uint256);

    function run() external returns (KUSDC token, TokenSwoopPayG payg, TokenSwoopPayGBatch paygBatch) {
        _checkChain();
        return _deploy(
            vm.envAddress("DEPLOYER_ADDRESS"),
            vm.envAddress("USER_ADDRESS"),
            vm.envAddress("PROVIDER_ADDRESS"),
            vm.envOr("PAYMENT_TOKEN_ADDRESS", address(0)),
            vm.envOr("EXISTING_PAYG_ADDRESS", address(0))
        );
    }

    function _checkChain() internal view {
        uint256 expected = expectedChainId();
        if (block.chainid != expected) revert WrongChain(expected, block.chainid);
    }

    function _deploy(address deployer, address user, address provider, address existingToken, address existingPayG)
        internal
        returns (KUSDC token, TokenSwoopPayG payg, TokenSwoopPayGBatch paygBatch)
    {
        _checkChain();
        if (
            deployer == address(0) || user == address(0) || provider == address(0) || user == provider
                || user == deployer || provider == deployer
        ) revert InvalidDemoWallets();
        if (existingPayG != address(0)) {
            if (existingToken == address(0) || address(TokenSwoopPayG(existingPayG).paymentToken()) != existingToken) {
                revert ExistingPayGTokenMismatch();
            }
            if (TokenSwoopPayG(existingPayG).provider() != provider) revert ExistingPayGProviderMismatch();
            // Prior deployments share the token/provider getters but still require a second authorization.
            (bool legacyABI,) = existingPayG.staticcall(abi.encodeWithSignature("getAuthorization(address)", user));
            if (legacyABI) revert ExistingPayGUnsupportedABI();
        }
        vm.startBroadcast(deployer);
        token = existingToken == address(0) ? new KUSDC(deployer) : KUSDC(existingToken);
        payg = existingPayG == address(0)
            ? new TokenSwoopPayG(address(token), deployer, provider, provider)
            : TokenSwoopPayG(existingPayG);
        paygBatch = new TokenSwoopPayGBatch(address(token), deployer, provider, provider);
        if (existingToken == address(0)) token.mint(user, 100e6);
        vm.stopBroadcast();
        // The CLI writes the manifest only after broadcast receipts and deployed code are confirmed.
    }
}

contract DeployTestnet is DeployBase {
    function expectedChainId() public pure override returns (uint256) {
        return 10143;
    }
}

contract DeployLocal is DeployBase {
    function expectedChainId() public pure override returns (uint256) {
        return 31337;
    }
}
