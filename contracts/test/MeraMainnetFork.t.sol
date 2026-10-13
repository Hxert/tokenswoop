// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {TokenSwoopMeraAccount} from "../src/TokenSwoopMeraAccount.sol";
import {TokenSwoopReserveVault} from "../src/TokenSwoopReserveVault.sol";

/// Read-only RPC fork. All balances, delegation and transfers below are local.
contract MeraMainnetForkTest is Test {
    function testMeraActionsAgainstDeployedUSDCAndReserveVault() public {
        string memory rpc = vm.envOr("MERA_FORK_RPC", string(""));
        if (bytes(rpc).length == 0) { vm.skip(true); return; }
        vm.createSelectFork(rpc);
        assertEq(block.chainid, 143);
        address implementation = vm.envAddress("MERA_FORK_IMPLEMENTATION");
        TokenSwoopMeraAccount deployed = TokenSwoopMeraAccount(payable(implementation));
        IERC20 token = deployed.paymentToken();
        TokenSwoopReserveVault reserve = TokenSwoopReserveVault(address(deployed.reserveContract()));
        uint256 key = uint256(keccak256("tokenswoop-mera-fork-test"));
        address user = vm.addr(key);
        vm.etch(user, implementation.code);
        deal(address(token), user, 1e6);
        TokenSwoopMeraAccount account = TokenSwoopMeraAccount(payable(user));
        perform(account, 0, 10000, 1);
        perform(account, 1, 10000, 2);
        perform(account, 2, 10000, 3);
        assertEq(reserve.principalOf(user), 10000);
        assertEq(token.balanceOf(user), 990000);
        assertEq(token.allowance(user, deployed.paymentContract()), 10000);
        perform(account, 3, 10000, 4);
        perform(account, 0, 0, 5);
        assertEq(reserve.principalOf(user), 0);
        assertEq(token.balanceOf(user), 1e6);
        assertEq(token.allowance(user, deployed.paymentContract()), 0);
        assertEq(user.balance, 0);
    }
    function perform(TokenSwoopMeraAccount account, uint8 action, uint256 amount, uint256 sequence) internal {
        uint256 deadline = block.timestamp + 600;
        bytes32 id = bytes32(sequence);
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(uint256(keccak256("tokenswoop-mera-fork-test")), account.operationDigest(action, amount, id, deadline));
        account.execute(action, amount, id, deadline, abi.encodePacked(r, s, v));
    }
}
