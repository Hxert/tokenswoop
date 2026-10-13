// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Script} from "forge-std/Script.sol";
import {TokenSwoopReserveDebt} from "../src/TokenSwoopReserveDebt.sol";
import {TokenSwoopPayGBatch} from "../src/TokenSwoopPayGBatch.sol";
import {TokenSwoopReserveVault} from "../src/TokenSwoopReserveVault.sol";

/// @dev Testnet deployment only. Mainnet activation is a separate release.
contract DeployReserveDebtTestnet is Script {
    function run() external returns (TokenSwoopPayGBatch payment,TokenSwoopReserveDebt reserve) {
        require(block.chainid==10143,"Monad Testnet only");
        address deployer=vm.envAddress("DEPLOYER_ADDRESS");
        address token=vm.envAddress("PAYMENT_TOKEN_ADDRESS");
        address pool=vm.envAddress("RESERVE_POOL_ADDRESS");
        address provider=vm.envAddress("PROVIDER_ADDRESS");
        address manager=vm.envAddress("RESERVE_MANAGER_ADDRESS");
        address stop=vm.envAddress("PAYMENT_STOP_AUTHORITY_ADDRESS");
        require(stop!=provider && stop!=address(0),"Separate stop authority required");
        vm.startBroadcast(deployer);
        payment=new TokenSwoopPayGBatch(token,stop,provider, provider);
        reserve=new TokenSwoopReserveDebt(token,pool,manager,vm.envAddress("SETTLEMENT_OPERATOR_ADDRESS"),vm.envAddress("TREASURY_ADDRESS"));
        vm.stopBroadcast();
    }
}

/// @dev Explicit mainnet release; simulate before --broadcast. No USDC seed funds required.
contract DeployReserveDebtMainnet is Script {
    address constant USDC = 0x754704Bc059F8C67012fEd69BC8A327a5aafb603;

    function run() external returns (TokenSwoopPayGBatch payment, TokenSwoopReserveVault reserve) {
        require(block.chainid == 143, "Monad Mainnet only");
        address deployer = vm.envAddress("MAINNET_DEPLOYER_ADDRESS");
        address provider = vm.envAddress("MAINNET_PAYMENT_PROVIDER");
        address recipient = vm.envAddress("MAINNET_PAYMENT_RECIPIENT");
        address stop = vm.envAddress("MAINNET_PAYMENT_STOP_AUTHORITY");
        address manager = vm.envAddress("MAINNET_RESERVE_MANAGER");
        address operator = vm.envAddress("MAINNET_RESERVE_OPERATOR");
        require(deployer != address(0) && stop != address(0) && stop != provider, "Invalid deployment roles");
        require(recipient != provider && recipient != address(0), "Separate revenue recipient required");
        vm.startBroadcast(deployer);
        payment = new TokenSwoopPayGBatch(USDC, stop, provider, recipient);
        reserve = new TokenSwoopReserveVault(USDC, manager, operator);
        vm.stopBroadcast();
    }
}

/// @dev Same custody model on testnet, with its configured six-decimal test token.
contract DeployReserveVaultTestnet is Script {
    function run() external returns (TokenSwoopPayGBatch payment, TokenSwoopReserveVault reserve) {
        require(block.chainid == 10143, "Monad Testnet only");
        address deployer = vm.envAddress("DEPLOYER_ADDRESS");
        address provider = vm.envAddress("PROVIDER_ADDRESS");
        address stop = vm.envAddress("PAYMENT_STOP_AUTHORITY_ADDRESS");
        address token = vm.envAddress("PAYMENT_TOKEN_ADDRESS");
        require(stop != provider && stop != address(0), "Separate stop authority required");
        vm.startBroadcast(deployer);
        payment = new TokenSwoopPayGBatch(token, stop, provider, vm.envAddress("PAYMENT_RECIPIENT_ADDRESS"));
        reserve = new TokenSwoopReserveVault(token, vm.envAddress("RESERVE_MANAGER_ADDRESS"), vm.envAddress("SETTLEMENT_OPERATOR_ADDRESS"));
        vm.stopBroadcast();
    }
}
