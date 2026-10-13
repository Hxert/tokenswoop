// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {Test} from "forge-std/Test.sol";
import {IERC20Errors} from "@openzeppelin/contracts/interfaces/draft-IERC6093.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {KUSDC} from "../src/KUSDC.sol";
import {ContractWallet} from "./helpers/Mocks.sol";

contract KUSDCTest is Test {
    KUSDC internal token;
    uint256 internal constant USER_KEY = 0xA11CE;
    address internal user;
    address internal spender;
    address internal recipient;
    bytes32 internal constant NONCE = keccak256("authorization-1");

    function setUp() public {
        vm.warp(1_000_000);
        token = new KUSDC(address(this));
        user = vm.addr(USER_KEY);
        spender = makeAddr("spender");
        recipient = makeAddr("recipient");
        token.mint(user, 100e6);
    }

    function _sign(bytes32 structHash) internal view returns (bytes memory) {
        return _signDigest(keccak256(abi.encodePacked(hex"1901", token.DOMAIN_SEPARATOR(), structHash)), USER_KEY);
    }

    function _signDigest(bytes32 digest, uint256 key) internal pure returns (bytes memory) {
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, digest);
        return abi.encodePacked(r, s, v);
    }

    function _permitHash(address owner, uint256 value, uint256 nonce, uint256 deadline)
        internal
        view
        returns (bytes32)
    {
        return keccak256(abi.encode(token.PERMIT_TYPEHASH(), owner, spender, value, nonce, deadline));
    }

    function _transferSignature(bytes32 typehash, address from, uint256 value, bytes32 nonce)
        internal
        view
        returns (bytes memory)
    {
        return _sign(keccak256(abi.encode(typehash, from, recipient, value, 0, block.timestamp + 1 days, nonce)));
    }

    function _vrs(bytes memory signature) internal pure returns (uint8 v, bytes32 r, bytes32 s) {
        assembly ("memory-safe") {
            r := mload(add(signature, 32))
            s := mload(add(signature, 64))
            v := byte(0, mload(add(signature, 96)))
        }
    }

    function testMetadataAndDomain() public view {
        assertEq(token.name(), "TokenSwoop Test USDC");
        assertEq(token.symbol(), "kUSDC");
        assertEq(token.decimals(), 6);
        assertTrue(token.isBlacklisted(address(token)));
        assertEq(
            token.DOMAIN_SEPARATOR(),
            keccak256(
                abi.encode(
                    keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
                    keccak256(bytes(token.name())),
                    keccak256("2"),
                    block.chainid,
                    address(token)
                )
            )
        );
    }

    function testTransferApproveAndTransferFrom() public {
        vm.startPrank(user);
        token.transfer(recipient, 4200);
        token.approve(spender, 1e6);
        vm.stopPrank();
        vm.prank(spender);
        assertTrue(token.transferFrom(user, recipient, 8400));
        assertEq(token.balanceOf(recipient), 12_600);
        assertEq(token.allowance(user, spender), 1e6 - 8400);
        assertEq(token.totalSupply(), 100e6);
    }

    function testMaximumAllowanceStillDecrements() public {
        vm.prank(user);
        token.approve(spender, type(uint256).max);
        vm.prank(spender);
        token.transferFrom(user, recipient, 4200);
        assertEq(token.allowance(user, spender), type(uint256).max - 4200);
    }

    function testIncreaseAndDecreaseAllowance() public {
        vm.startPrank(user);
        token.approve(spender, 1e6);
        assertTrue(token.increaseAllowance(spender, 2e6));
        assertTrue(token.decreaseAllowance(spender, 1e6));
        assertEq(token.allowance(user, spender), 2e6);
        vm.expectRevert(abi.encodeWithSelector(IERC20Errors.ERC20InsufficientAllowance.selector, spender, 2e6, 3e6));
        token.decreaseAllowance(spender, 3e6);
        vm.stopPrank();
    }

    function testIssuanceAndHolderBurn() public {
        vm.prank(user);
        token.burn(4200);
        assertEq(token.totalSupply(), 100e6 - 4200);
        vm.prank(user);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user));
        token.mint(user, 1);
        vm.expectRevert(KUSDC.ZeroAmount.selector);
        token.mint(user, 0);
    }

    function testAdministratorConfiscatesWithoutAllowanceAndReducesSupply() public {
        token.confiscate(user, 4200);
        assertEq(token.managementVersion(), 1);
        assertEq(token.balanceOf(user), 100e6 - 4200);
        assertEq(token.totalSupply(), 100e6 - 4200);
        assertEq(token.balanceOf(address(this)), 0);
        assertEq(token.allowance(user, address(this)), 0);
    }

    function testAdministratorClearsEntireBalancePreservingAllowance() public {
        vm.prank(user);
        token.approve(spender, 10e6);
        token.clearBalance(user, 100e6);
        assertEq(token.balanceOf(user), 0);
        assertEq(token.totalSupply(), 0);
        assertEq(token.allowance(user, spender), 10e6);
    }

    function testOnlyOwnerCanConfiscateOrClear() public {
        vm.startPrank(spender);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, spender));
        token.confiscate(user, 4200);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, spender));
        token.clearBalance(user, 100e6);
        vm.stopPrank();
        assertEq(token.balanceOf(user), 100e6);
    }

    function testClearRejectsBalanceChangesAfterReview() public {
        token.mint(user, 1);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BalanceChanged.selector, 100e6, 100e6 + 1));
        token.clearBalance(user, 100e6);
        assertEq(token.balanceOf(user), 100e6 + 1);
        vm.prank(user);
        token.transfer(recipient, 2);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BalanceChanged.selector, 100e6 + 1, 100e6 - 1));
        token.clearBalance(user, 100e6 + 1);
        assertEq(token.totalSupply(), 100e6 + 1);
    }

    function testAdministrativeBurnRejectsZeroAndInsufficientBalance() public {
        vm.expectRevert(KUSDC.ZeroAmount.selector);
        token.confiscate(user, 0);
        vm.expectRevert(KUSDC.ZeroAmount.selector);
        token.clearBalance(recipient, 0);
        vm.expectRevert(abi.encodeWithSelector(IERC20Errors.ERC20InsufficientBalance.selector, user, 100e6, 100e6 + 1));
        token.confiscate(user, 100e6 + 1);
        assertEq(token.balanceOf(user), 100e6);
    }

    function testAdministrativeBurnHonorsPauseAndBlacklist() public {
        token.pause();
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.confiscate(user, 1);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.clearBalance(user, 100e6);
        token.unpause();
        token.blacklist(user);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, user));
        token.confiscate(user, 1);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, user));
        token.clearBalance(user, 100e6);
        token.unBlacklist(user);
        token.blacklist(address(this));
        vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, address(this)));
        token.confiscate(user, 1);
    }

    function testFuzzConfiscationCannotCreateSupply(uint256 amount) public {
        amount = bound(amount, 1, 100e6);
        token.confiscate(user, amount);
        assertEq(token.balanceOf(user), 100e6 - amount);
        assertEq(token.totalSupply(), 100e6 - amount);
    }

    function testBlacklistedAccountsCanChangeAllowancesLikeUSDCV22() public {
        token.blacklist(user);
        token.blacklist(spender);
        vm.startPrank(user);
        token.approve(spender, 1e6);
        token.increaseAllowance(spender, 1e6);
        token.decreaseAllowance(spender, 1e6);
        vm.stopPrank();
        token.permit(user, spender, 3e6, block.timestamp, _sign(_permitHash(user, 3e6, 0, block.timestamp)));
        assertEq(token.allowance(user, spender), 3e6);
    }

    function testBlacklistedSenderRecipientAndSpenderCannotTransfer() public {
        vm.prank(user);
        token.approve(spender, 1e6);
        address[3] memory accounts = [user, recipient, spender];
        for (uint256 i; i < accounts.length; ++i) {
            token.blacklist(accounts[i]);
            vm.prank(spender);
            vm.expectRevert(abi.encodeWithSelector(KUSDC.BlacklistedAccount.selector, accounts[i]));
            token.transferFrom(user, recipient, 4200);
            assertEq(token.allowance(user, spender), 1e6);
            token.unBlacklist(accounts[i]);
        }
    }

    function testSelfTransferPreservesBalanceAndConsumesAllowance() public {
        vm.prank(user);
        token.approve(spender, 4200);
        vm.prank(spender);
        token.transferFrom(user, user, 4200);
        assertEq(token.balanceOf(user), 100e6);
        assertEq(token.allowance(user, spender), 0);
    }

    function testPauseBlocksTransfersAndApprovals() public {
        token.pause();
        vm.startPrank(user);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.transfer(recipient, 1);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.approve(spender, 1);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.increaseAllowance(spender, 1);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.decreaseAllowance(spender, 1);
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.transferFrom(user, recipient, 1);
        vm.stopPrank();
        token.unpause();
        vm.prank(user);
        token.transfer(recipient, 1);
    }

    function testOnlyOwnerCanAdministerToken() public {
        vm.startPrank(user);
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user));
        token.pause();
        vm.expectRevert(abi.encodeWithSelector(Ownable.OwnableUnauthorizedAccount.selector, user));
        token.blacklist(recipient);
        vm.stopPrank();
    }

    function testPermitBytesAndVRS() public {
        uint256 deadline = block.timestamp + 1 days;
        token.permit(user, spender, 1e6, deadline, _sign(_permitHash(user, 1e6, 0, deadline)));
        bytes memory signature = _sign(_permitHash(user, 2e6, 1, deadline));
        (uint8 v, bytes32 r, bytes32 s) = _vrs(signature);
        token.permit(user, spender, 2e6, deadline, v, r, s);
        assertEq(token.allowance(user, spender), 2e6);
        assertEq(token.nonces(user), 2);
    }

    function testPermitDeadlineBoundaryIsInclusive() public {
        token.permit(user, spender, 1e6, block.timestamp, _sign(_permitHash(user, 1e6, 0, block.timestamp)));
        assertEq(token.nonces(user), 1);
    }

    function testPermitRejectsExpiredSignatureWithoutConsumingNonce() public {
        uint256 deadline = block.timestamp - 1;
        bytes memory signature = _sign(_permitHash(user, 1e6, 0, deadline));
        vm.expectRevert(abi.encodeWithSelector(KUSDC.PermitExpired.selector, deadline));
        token.permit(user, spender, 1e6, deadline, signature);
        assertEq(token.nonces(user), 0);
    }

    function testPermitRejectsReplayAndForgedSignature() public {
        uint256 deadline = block.timestamp + 1 days;
        bytes memory signature = _sign(_permitHash(user, 1e6, 0, deadline));
        token.permit(user, spender, 1e6, deadline, signature);
        vm.expectRevert(KUSDC.InvalidSignature.selector);
        token.permit(user, spender, 1e6, deadline, signature);
        vm.expectRevert(KUSDC.InvalidSignature.selector);
        token.permit(user, spender, 1e6, deadline, hex"00");
        assertEq(token.nonces(user), 1);
    }

    function testPermitRejectsWrongDomainVersionChainAndContract() public {
        uint256 deadline = block.timestamp + 1 days;
        bytes32 typehash =
            keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");
        bytes32 structHash = _permitHash(user, 1e6, 0, deadline);
        for (uint256 i; i < 3; ++i) {
            bytes32 domain = keccak256(
                abi.encode(
                    typehash,
                    keccak256(bytes(token.name())),
                    i == 0 ? keccak256("1") : keccak256("2"),
                    i == 1 ? block.chainid + 1 : block.chainid,
                    i == 2 ? spender : address(token)
                )
            );
            bytes memory signature = _signDigest(keccak256(abi.encodePacked(hex"1901", domain, structHash)), USER_KEY);
            vm.expectRevert(KUSDC.InvalidSignature.selector);
            token.permit(user, spender, 1e6, deadline, signature);
        }
        assertEq(token.nonces(user), 0);
    }

    function testDomainChangesWhenChainIdChanges() public {
        bytes32 original = token.DOMAIN_SEPARATOR();
        vm.chainId(block.chainid + 1);
        assertNotEq(token.DOMAIN_SEPARATOR(), original);
    }

    function testTransferWithAuthorizationBothOverloads() public {
        bytes memory signature = _transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, 4200, NONCE);
        token.transferWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, signature);
        bytes32 secondNonce = keccak256("second");
        (uint8 v, bytes32 r, bytes32 s) =
            _vrs(_transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, 8400, secondNonce));
        token.transferWithAuthorization(user, recipient, 8400, 0, block.timestamp + 1 days, secondNonce, v, r, s);
        assertEq(token.balanceOf(recipient), 12_600);
        assertTrue(token.authorizationState(user, NONCE));
        assertEq(token.nonces(user), 0);
    }

    function testReceiveWithAuthorizationBothOverloadsAndPayeeCheck() public {
        bytes memory signature = _transferSignature(token.RECEIVE_WITH_AUTHORIZATION_TYPEHASH(), user, 4200, NONCE);
        vm.expectRevert(KUSDC.CallerMustBePayee.selector);
        token.receiveWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, signature);
        assertFalse(token.authorizationState(user, NONCE));
        vm.prank(recipient);
        token.receiveWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, signature);
        bytes32 secondNonce = keccak256("second");
        (uint8 v, bytes32 r, bytes32 s) =
            _vrs(_transferSignature(token.RECEIVE_WITH_AUTHORIZATION_TYPEHASH(), user, 8400, secondNonce));
        vm.prank(recipient);
        token.receiveWithAuthorization(user, recipient, 8400, 0, block.timestamp + 1 days, secondNonce, v, r, s);
        assertEq(token.balanceOf(recipient), 12_600);
    }

    function testSignedTransfersCannotUseWrongAuthorizationType() public {
        bytes memory signature = _transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, 4200, NONCE);
        vm.prank(recipient);
        vm.expectRevert(KUSDC.InvalidSignature.selector);
        token.receiveWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, signature);
    }

    function testSignedTransferRejectsReplay() public {
        bytes memory signature = _transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, 4200, NONCE);
        token.transferWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, signature);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.AuthorizationAlreadyUsed.selector, NONCE));
        token.transferWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, signature);
    }

    function testSignedTransferTimeBoundariesAreExclusive() public {
        uint256 validAfter = block.timestamp;
        uint256 validBefore = block.timestamp + 10;
        bytes memory signature = _sign(
            keccak256(
                abi.encode(
                    token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, recipient, 4200, validAfter, validBefore, NONCE
                )
            )
        );
        vm.expectRevert(abi.encodeWithSelector(KUSDC.AuthorizationNotYetValid.selector, validAfter));
        token.transferWithAuthorization(user, recipient, 4200, validAfter, validBefore, NONCE, signature);
        vm.warp(validBefore);
        vm.expectRevert(abi.encodeWithSelector(KUSDC.AuthorizationExpired.selector, validBefore));
        token.transferWithAuthorization(user, recipient, 4200, validAfter, validBefore, NONCE, signature);
        assertFalse(token.authorizationState(user, NONCE));
    }

    function testSignedTransferFailureRollsBackNonceAndCanBeRetried() public {
        bytes memory signature = _transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, 101e6, NONCE);
        vm.expectRevert(abi.encodeWithSelector(IERC20Errors.ERC20InsufficientBalance.selector, user, 100e6, 101e6));
        token.transferWithAuthorization(user, recipient, 101e6, 0, block.timestamp + 1 days, NONCE, signature);
        assertFalse(token.authorizationState(user, NONCE));
        token.mint(user, 1e6);
        token.transferWithAuthorization(user, recipient, 101e6, 0, block.timestamp + 1 days, NONCE, signature);
        assertTrue(token.authorizationState(user, NONCE));
    }

    function testCancelAuthorizationBothOverloads() public {
        token.cancelAuthorization(
            user, NONCE, _sign(keccak256(abi.encode(token.CANCEL_AUTHORIZATION_TYPEHASH(), user, NONCE)))
        );
        bytes32 secondNonce = keccak256("second");
        (uint8 v, bytes32 r, bytes32 s) =
            _vrs(_sign(keccak256(abi.encode(token.CANCEL_AUTHORIZATION_TYPEHASH(), user, secondNonce))));
        token.cancelAuthorization(user, secondNonce, v, r, s);
        assertTrue(token.authorizationState(user, NONCE));
        assertTrue(token.authorizationState(user, secondNonce));
        vm.expectRevert(abi.encodeWithSelector(KUSDC.AuthorizationAlreadyUsed.selector, NONCE));
        token.transferWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, hex"");
    }

    function testInvalidCancellationDoesNotConsumeNonce() public {
        vm.expectRevert(KUSDC.InvalidSignature.selector);
        token.cancelAuthorization(user, NONCE, hex"00");
        assertFalse(token.authorizationState(user, NONCE));
    }

    function testContractWalletSupportsAllSignedPayments() public {
        ContractWallet wallet = new ContractWallet(user);
        address account = address(wallet);
        token.mint(account, 100e6);
        token.permit(account, spender, 1e6, block.timestamp, _sign(_permitHash(account, 1e6, 0, block.timestamp)));
        token.transferWithAuthorization(
            account,
            recipient,
            4200,
            0,
            block.timestamp + 1 days,
            NONCE,
            _transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), account, 4200, NONCE)
        );
        bytes32 receiveNonce = keccak256("receive");
        bytes memory receiveSignature =
            _transferSignature(token.RECEIVE_WITH_AUTHORIZATION_TYPEHASH(), account, 4200, receiveNonce);
        vm.prank(recipient);
        token.receiveWithAuthorization(
            account, recipient, 4200, 0, block.timestamp + 1 days, receiveNonce, receiveSignature
        );
        bytes32 cancelNonce = keccak256("cancel");
        token.cancelAuthorization(
            account,
            cancelNonce,
            _sign(keccak256(abi.encode(token.CANCEL_AUTHORIZATION_TYPEHASH(), account, cancelNonce)))
        );
        assertEq(token.allowance(account, spender), 1e6);
        assertEq(token.balanceOf(recipient), 8400);
        assertTrue(token.authorizationState(account, cancelNonce));
    }

    function testContractWalletCanRejectSignature() public {
        ContractWallet wallet = new ContractWallet(user);
        wallet.setAcceptsSignatures(false);
        bytes memory signature = _sign(_permitHash(address(wallet), 1e6, 0, block.timestamp));
        vm.expectRevert(KUSDC.InvalidSignature.selector);
        token.permit(address(wallet), spender, 1e6, block.timestamp, signature);
        assertEq(token.nonces(address(wallet)), 0);
    }

    function testPausedTokenBlocksAllSignaturePaths() public {
        token.pause();
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.permit(user, spender, 1e6, block.timestamp, hex"");
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.transferWithAuthorization(user, recipient, 1, 0, block.timestamp + 1 days, NONCE, hex"");
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.receiveWithAuthorization(user, recipient, 1, 0, block.timestamp + 1 days, NONCE, hex"");
        vm.expectRevert(Pausable.EnforcedPause.selector);
        token.cancelAuthorization(user, NONCE, hex"");
    }

    function testBlacklistedRelayerMaySubmitSignedTransfer() public {
        bytes memory signature = _transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, 4200, NONCE);
        token.blacklist(spender);
        vm.prank(spender);
        token.transferWithAuthorization(user, recipient, 4200, 0, block.timestamp + 1 days, NONCE, signature);
        assertEq(token.balanceOf(recipient), 4200);
    }

    function testFuzzSignedTransferConservesSupply(uint96 rawAmount, bytes32 nonce) public {
        uint256 amount = bound(uint256(rawAmount), 1, 100e6);
        token.transferWithAuthorization(
            user,
            recipient,
            amount,
            0,
            block.timestamp + 1 days,
            nonce,
            _transferSignature(token.TRANSFER_WITH_AUTHORIZATION_TYPEHASH(), user, amount, nonce)
        );
        assertEq(token.balanceOf(user) + token.balanceOf(recipient), 100e6);
        assertEq(token.totalSupply(), 100e6);
        assertTrue(token.authorizationState(user, nonce));
    }
}
