// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Script} from "forge-std/Script.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {TokenSwoopReserve} from "../src/TokenSwoopReserve.sol";
import {TestnetAavePool} from "../src/testnet/TestnetAavePool.sol";

abstract contract DeployReserveBase is Script {
    error WrongChain(uint256 expected, uint256 actual);
    error InvalidConfiguration();
    error NotTestTokenOwner();

    function expectedChainId() public pure virtual returns (uint256);

    function run() external returns (TestnetAavePool pool, TokenSwoopReserve reserve) {
        _checkChain(); // Always checked BEFORE reading signing configuration.
        return _deploy(
            vm.envAddress("DEPLOYER_ADDRESS"),
            vm.envAddress("PAYMENT_TOKEN_ADDRESS"),
            vm.envAddress("SETTLEMENT_OPERATOR_ADDRESS")
        );
    }

    function _checkChain() internal view {
        if (block.chainid != expectedChainId()) revert WrongChain(expectedChainId(), block.chainid);
    }

    function _deploy(address deployer, address token, address operator)
        internal
        returns (TestnetAavePool pool, TokenSwoopReserve reserve)
    {
        _checkChain();
        if (deployer == address(0) || operator == address(0) || token.code.length == 0) revert InvalidConfiguration();
        if (KUSDC(token).owner() != deployer) revert NotTestTokenOwner();
        vm.startBroadcast(deployer);
        pool = new TestnetAavePool(token, deployer);
        reserve = new TokenSwoopReserve(token, address(pool), deployer, operator, deployer);
        // Platform capital, separate from user principal; prevents Aave rounding from
        // silently creating an underfunded nominal USDC balance.
        KUSDC(token).mint(deployer, 1e6);
        KUSDC(token).approve(address(reserve), 1e6);
        reserve.fundBuffer(1e6);
        vm.stopBroadcast();
    }
}

contract DeployReserveTestnet is DeployReserveBase {
    function expectedChainId() public pure override returns (uint256) {
        return 10143;
    }
}

contract DeployReserveLocal is DeployReserveBase {
    function expectedChainId() public pure override returns (uint256) {
        return 31337;
    }
}
