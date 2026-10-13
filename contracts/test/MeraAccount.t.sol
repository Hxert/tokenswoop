// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {TokenSwoopMeraAccount} from "../src/TokenSwoopMeraAccount.sol";
import {TokenSwoopReserveVault} from "../src/TokenSwoopReserveVault.sol";

contract MeraTestToken is ERC20 {
    constructor() ERC20("Test USD", "USD") {}
    function decimals() public pure override returns (uint8) { return 6; }
    function mint(address to, uint256 amount) external { _mint(to, amount); }
}

contract MeraAccountTest is Test {
    MeraTestToken token;
    TokenSwoopReserveVault reserve;
    TokenSwoopMeraAccount implementation;
    TokenSwoopMeraAccount account;
    uint256 key = 0x1234;
    address user;
    address manager = address(101);
    address operator = address(102);
    address payment;
    uint256 sequence;

    function setUp() public {
        user = vm.addr(key);
        token = new MeraTestToken();
        payment = address(new MeraTestToken());
        reserve = new TokenSwoopReserveVault(address(token), manager, operator);
        implementation = new TokenSwoopMeraAccount(address(token), payment, address(reserve));
        // Exercise the exact deployed runtime in the EOA storage/address context.
        // Actual type-4 delegation is covered separately on Monad testnet.
        vm.etch(user, address(implementation).code);
        account = TokenSwoopMeraAccount(payable(user));
        token.mint(user, 1000e6);
    }

    function signature(uint8 action, uint256 amount, bytes32 id, uint256 deadline) internal view returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, account.operationDigest(action, amount, id, deadline));
        return abi.encodePacked(r, s, v);
    }
    function perform(uint8 action, uint256 amount) internal returns (bytes32 id) {
        id = bytes32(++sequence);
        uint256 deadline = block.timestamp + 600;
        account.execute(action, amount, id, deadline, signature(action, amount, id, deadline));
    }
    function testAllOperationsPreserveOriginalAddressAndIndependentAllowances() public {
        perform(0, 10e6);
        perform(1, 50e6);
        perform(2, 50e6);
        assertEq(token.allowance(user, payment), 10e6);
        assertEq(reserve.principalOf(user), 50e6);
        perform(3, 50e6);
        assertEq(token.balanceOf(user), 1000e6);
        assertEq(reserve.principalOf(user), 0);
        perform(0, 0);
        assertEq(token.allowance(user, payment), 0);
        assertEq(user.balance, 0);
    }
    function testReplayAndTamperingRejected() public {
        bytes32 id = bytes32(uint256(7));
        uint256 deadline = block.timestamp + 600;
        bytes memory sig = signature(0, 10e6, id, deadline);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidSignature.selector);
        account.execute(0, 11e6, id, deadline, sig);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidSignature.selector);
        account.execute(1, 10e6, id, deadline, sig);
        account.execute(0, 10e6, id, deadline, sig);
        vm.expectRevert(TokenSwoopMeraAccount.AlreadyExecuted.selector);
        account.execute(0, 10e6, id, deadline, sig);
    }
    function testOtherWalletChainImplementationAndExpiredSignaturesFail() public {
        bytes32 id = bytes32(uint256(1));
        uint256 deadline = block.timestamp + 600;
        bytes memory sig = signature(0, 1, id, deadline);
        address other = vm.addr(0x5678);
        vm.etch(other, address(implementation).code);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidSignature.selector);
        TokenSwoopMeraAccount(payable(other)).execute(0, 1, id, deadline, sig);
        vm.chainId(block.chainid + 1);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidSignature.selector);
        account.execute(0, 1, id, deadline, sig);
        vm.chainId(block.chainid - 1);
        TokenSwoopMeraAccount replacement = new TokenSwoopMeraAccount(address(token), payment, address(reserve));
        vm.etch(user, address(replacement).code);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidSignature.selector);
        account.execute(0, 1, id, deadline, sig);
        vm.warp(deadline + 1);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidOperation.selector);
        account.execute(0, 1, id, deadline, sig);
    }
    function testFreezeStillAppliesAndFailedCallDoesNotConsumeID() public {
        perform(1, 50e6); perform(2, 50e6);
        bytes32[] memory ids = new bytes32[](1); ids[0] = bytes32(uint256(100));
        uint256[] memory amounts = new uint256[](1); amounts[0] = 5e6;
        vm.prank(operator); reserve.reportDebts(user, ids, amounts, 0);
        bytes32 id = bytes32(uint256(99)); uint256 deadline = block.timestamp + 600;
        bytes memory sig = signature(3, 50e6, id, deadline);
        vm.expectRevert(); account.execute(3, 50e6, id, deadline, sig);
        assertFalse(account.operationUsed(id));
        perform(3, 45e6);
        assertEq(reserve.principalOf(user), 5e6);
        vm.prank(manager); reserve.clearDebt(user);
        perform(3, 5e6);
    }
    function testNoPlatformAuthorityOrDirectImplementationExecution() public {
        vm.expectRevert(); account.execute(3, 1, bytes32(uint256(1)), block.timestamp + 600, new bytes(65));
        vm.expectRevert(TokenSwoopMeraAccount.InvalidOperation.selector);
        implementation.execute(0, 1, bytes32(uint256(1)), block.timestamp + 600, new bytes(65));
        bytes32 id = bytes32(uint256(5)); uint256 deadline = block.timestamp + 600;
        bytes memory invalidAction = signature(4, 1, id, deadline);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidOperation.selector);
        account.execute(4, 1, id, deadline, invalidAction);
        bytes memory excessive = signature(0, 100e6 + 1, id, deadline);
        vm.expectRevert(TokenSwoopMeraAccount.InvalidOperation.selector);
        account.execute(0, 100e6 + 1, id, deadline, excessive);
    }
    function testEOAOwnerCanStillActDirectlyAndValidateContractSignatures() public {
        perform(1, 20e6); perform(2, 20e6);
        // The owner can recover through an ordinary EOA transaction without a relayer.
        vm.prank(user); reserve.withdraw(20e6);
        assertEq(reserve.principalOf(user), 0);
        bytes32 digest = keccak256("wallet login or external typed data");
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, digest);
        bytes memory sig = abi.encodePacked(r, s, v);
        assertEq(account.isValidSignature(digest, sig), bytes4(0x1626ba7e));
        assertEq(account.isValidSignature(keccak256("tampered"), sig), bytes4(0xffffffff));
        assertEq(account.isValidSignature(digest, hex"1234"), bytes4(0xffffffff));
        assertEq(implementation.isValidSignature(digest, sig), bytes4(0xffffffff));
    }
    function testFuzzRepeatedWithdrawalCannotExceedPrincipal(uint96 amount) public {
        uint256 n = bound(amount, 1, 1000e6);
        perform(1, n); perform(2, n); perform(3, n);
        bytes32 id = bytes32(++sequence); uint256 deadline = block.timestamp + 600;
        bytes memory sig = signature(3, n, id, deadline);
        vm.expectRevert(); account.execute(3, n, id, deadline, sig);
        assertEq(reserve.principalOf(user), 0);
        assertEq(token.balanceOf(user), 1000e6);
    }
}
