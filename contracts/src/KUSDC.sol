// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";
import {Ownable2Step} from "@openzeppelin/contracts/access/Ownable2Step.sol";
import {Pausable} from "@openzeppelin/contracts/utils/Pausable.sol";
import {EIP712} from "@openzeppelin/contracts/utils/cryptography/EIP712.sol";
import {SignatureChecker} from "@openzeppelin/contracts/utils/cryptography/SignatureChecker.sol";

/// @notice Test-only token that mirrors Circle FiatTokenV2_2 payment semantics.
/// @dev Simplified issuance/administration; no proxy, USD backing, or cross-chain issuance.
contract KUSDC is ERC20, Ownable2Step, Pausable, EIP712 {
    bytes32 public constant PERMIT_TYPEHASH =
        keccak256("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)");
    bytes32 public constant TRANSFER_WITH_AUTHORIZATION_TYPEHASH = keccak256(
        "TransferWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"
    );
    bytes32 public constant RECEIVE_WITH_AUTHORIZATION_TYPEHASH = keccak256(
        "ReceiveWithAuthorization(address from,address to,uint256 value,uint256 validAfter,uint256 validBefore,bytes32 nonce)"
    );
    bytes32 public constant CANCEL_AUTHORIZATION_TYPEHASH =
        keccak256("CancelAuthorization(address authorizer,bytes32 nonce)");
    uint256 private constant MAX_BALANCE = (uint256(1) << 255) - 1;

    mapping(address account => bool) public isBlacklisted;
    mapping(address owner => uint256) private _permitNonces;
    mapping(address authorizer => mapping(bytes32 nonce => bool)) private _authorizationStates;

    error BlacklistedAccount(address account);
    error InvalidSignature();
    error PermitExpired(uint256 deadline);
    error AuthorizationNotYetValid(uint256 validAfter);
    error AuthorizationExpired(uint256 validBefore);
    error AuthorizationAlreadyUsed(bytes32 nonce);
    error CallerMustBePayee();
    error BalanceLimitExceeded();
    error ZeroAmount();
    error BalanceChanged(uint256 expected, uint256 actual);

    event Blacklisted(address indexed account);
    event UnBlacklisted(address indexed account);
    event AuthorizationUsed(address indexed authorizer, bytes32 indexed nonce);
    event AuthorizationCanceled(address indexed authorizer, bytes32 indexed nonce);
    event Confiscated(address indexed operator, address indexed account, uint256 amount);
    event BalanceCleared(address indexed operator, address indexed account, uint256 amount);

    constructor(address initialOwner)
        ERC20("TokenSwoop Test USDC", "kUSDC")
        Ownable(initialOwner)
        EIP712("TokenSwoop Test USDC", "2")
    {
        // Circle's v2.2 initialization also blacklists the token contract itself.
        isBlacklisted[address(this)] = true;
        emit Blacklisted(address(this));
    }

    function decimals() public pure override returns (uint8) {
        return 6;
    }

    function mint(address to, uint256 amount) external onlyOwner {
        if (amount == 0) revert ZeroAmount();
        _requireNotBlacklisted(msg.sender);
        _mint(to, amount);
    }

    function burn(uint256 amount) external {
        if (amount == 0) revert ZeroAmount();
        _burn(msg.sender, amount);
    }

    /// @notice Identifies the test-token administration interface used by the standalone console.
    function managementVersion() external pure returns (uint256) {
        return 1;
    }

    /// @notice Destroy an exact quantity of test tokens without a holder allowance.
    function confiscate(address account, uint256 amount) external onlyOwner {
        if (amount == 0) revert ZeroAmount();
        _requireNotBlacklisted(msg.sender);
        _burn(account, amount);
        emit Confiscated(msg.sender, account, amount);
    }

    /// @notice Destroy the entire reviewed balance; reject a balance that changed after review.
    function clearBalance(address account, uint256 expectedBalance) external onlyOwner {
        uint256 current = balanceOf(account);
        if (current != expectedBalance) revert BalanceChanged(expectedBalance, current);
        if (current == 0) revert ZeroAmount();
        _requireNotBlacklisted(msg.sender);
        _burn(account, current);
        emit BalanceCleared(msg.sender, account, current);
    }

    function pause() external onlyOwner {
        _pause();
    }

    function unpause() external onlyOwner {
        _unpause();
    }

    function blacklist(address account) external onlyOwner {
        isBlacklisted[account] = true;
        emit Blacklisted(account);
    }

    function unBlacklist(address account) external onlyOwner {
        isBlacklisted[account] = false;
        emit UnBlacklisted(account);
    }

    // FiatTokenV2_2 permits approval changes by blacklisted accounts, but not while paused.
    function approve(address spender, uint256 value) public override whenNotPaused returns (bool) {
        return super.approve(spender, value);
    }

    function increaseAllowance(address spender, uint256 increment) external whenNotPaused returns (bool) {
        _approve(msg.sender, spender, allowance(msg.sender, spender) + increment);
        return true;
    }

    function decreaseAllowance(address spender, uint256 decrement) external whenNotPaused returns (bool) {
        uint256 current = allowance(msg.sender, spender);
        if (current < decrement) revert ERC20InsufficientAllowance(spender, current, decrement);
        _approve(msg.sender, spender, current - decrement);
        return true;
    }

    function transferFrom(address from, address to, uint256 value) public override whenNotPaused returns (bool) {
        _requireNotBlacklisted(msg.sender);
        return super.transferFrom(from, to, value);
    }

    function nonces(address owner) external view returns (uint256) {
        return _permitNonces[owner];
    }

    function DOMAIN_SEPARATOR() external view returns (bytes32) {
        return _domainSeparatorV4();
    }

    function authorizationState(address authorizer, bytes32 nonce) external view returns (bool) {
        return _authorizationStates[authorizer][nonce];
    }

    function permit(address owner, address spender, uint256 value, uint256 deadline, uint8 v, bytes32 r, bytes32 s)
        external
        whenNotPaused
    {
        _permit(owner, spender, value, deadline, abi.encodePacked(r, s, v));
    }

    function permit(address owner, address spender, uint256 value, uint256 deadline, bytes calldata signature)
        external
        whenNotPaused
    {
        _permit(owner, spender, value, deadline, signature);
    }

    function transferWithAuthorization(
        address from,
        address to,
        uint256 value,
        uint256 validAfter,
        uint256 validBefore,
        bytes32 nonce,
        uint8 v,
        bytes32 r,
        bytes32 s
    ) external whenNotPaused {
        _transferAuthorization(
            from,
            to,
            value,
            validAfter,
            validBefore,
            nonce,
            abi.encodePacked(r, s, v),
            TRANSFER_WITH_AUTHORIZATION_TYPEHASH
        );
    }

    function transferWithAuthorization(
        address from,
        address to,
        uint256 value,
        uint256 validAfter,
        uint256 validBefore,
        bytes32 nonce,
        bytes calldata signature
    ) external whenNotPaused {
        _transferAuthorization(
            from, to, value, validAfter, validBefore, nonce, signature, TRANSFER_WITH_AUTHORIZATION_TYPEHASH
        );
    }

    function receiveWithAuthorization(
        address from,
        address to,
        uint256 value,
        uint256 validAfter,
        uint256 validBefore,
        bytes32 nonce,
        uint8 v,
        bytes32 r,
        bytes32 s
    ) external whenNotPaused {
        if (to != msg.sender) revert CallerMustBePayee();
        _transferAuthorization(
            from,
            to,
            value,
            validAfter,
            validBefore,
            nonce,
            abi.encodePacked(r, s, v),
            RECEIVE_WITH_AUTHORIZATION_TYPEHASH
        );
    }

    function receiveWithAuthorization(
        address from,
        address to,
        uint256 value,
        uint256 validAfter,
        uint256 validBefore,
        bytes32 nonce,
        bytes calldata signature
    ) external whenNotPaused {
        if (to != msg.sender) revert CallerMustBePayee();
        _transferAuthorization(
            from, to, value, validAfter, validBefore, nonce, signature, RECEIVE_WITH_AUTHORIZATION_TYPEHASH
        );
    }

    function cancelAuthorization(address authorizer, bytes32 nonce, uint8 v, bytes32 r, bytes32 s)
        external
        whenNotPaused
    {
        _cancelAuthorization(authorizer, nonce, abi.encodePacked(r, s, v));
    }

    function cancelAuthorization(address authorizer, bytes32 nonce, bytes calldata signature) external whenNotPaused {
        _cancelAuthorization(authorizer, nonce, signature);
    }

    function _permit(address owner, address spender, uint256 value, uint256 deadline, bytes memory signature) private {
        if (block.timestamp > deadline) revert PermitExpired(deadline);
        bytes32 digest = _hashTypedDataV4(
            keccak256(abi.encode(PERMIT_TYPEHASH, owner, spender, value, _permitNonces[owner], deadline))
        );
        _requireValidSignature(owner, digest, signature);
        ++_permitNonces[owner];
        _approve(owner, spender, value);
    }

    function _transferAuthorization(
        address from,
        address to,
        uint256 value,
        uint256 validAfter,
        uint256 validBefore,
        bytes32 nonce,
        bytes memory signature,
        bytes32 typehash
    ) private {
        _requireNotBlacklisted(from);
        _requireNotBlacklisted(to);
        if (block.timestamp <= validAfter) revert AuthorizationNotYetValid(validAfter);
        if (block.timestamp >= validBefore) revert AuthorizationExpired(validBefore);
        _requireUnusedAuthorization(from, nonce);
        bytes32 digest =
            _hashTypedDataV4(keccak256(abi.encode(typehash, from, to, value, validAfter, validBefore, nonce)));
        _requireValidSignature(from, digest, signature);
        _authorizationStates[from][nonce] = true;
        emit AuthorizationUsed(from, nonce);
        _transfer(from, to, value);
    }

    function _cancelAuthorization(address authorizer, bytes32 nonce, bytes memory signature) private {
        _requireUnusedAuthorization(authorizer, nonce);
        _requireValidSignature(
            authorizer,
            _hashTypedDataV4(keccak256(abi.encode(CANCEL_AUTHORIZATION_TYPEHASH, authorizer, nonce))),
            signature
        );
        _authorizationStates[authorizer][nonce] = true;
        emit AuthorizationCanceled(authorizer, nonce);
    }

    function _requireValidSignature(address signer, bytes32 digest, bytes memory signature) private view {
        if (signer == address(0) || !SignatureChecker.isValidSignatureNow(signer, digest, signature)) {
            revert InvalidSignature();
        }
    }

    function _requireUnusedAuthorization(address authorizer, bytes32 nonce) private view {
        if (_authorizationStates[authorizer][nonce]) revert AuthorizationAlreadyUsed(nonce);
    }

    function _requireNotBlacklisted(address account) private view {
        if (isBlacklisted[account]) revert BlacklistedAccount(account);
    }

    function _update(address from, address to, uint256 value) internal override whenNotPaused {
        if (from != address(0)) _requireNotBlacklisted(from);
        if (to != address(0)) _requireNotBlacklisted(to);
        if (to != address(0) && from != to) {
            if (value > MAX_BALANCE || balanceOf(to) > MAX_BALANCE - value) revert BalanceLimitExceeded();
        }
        super._update(from, to, value);
    }

    // Unlike OpenZeppelin ERC20, Circle decrements even a max-uint allowance.
    function _spendAllowance(address owner, address spender, uint256 value) internal override {
        uint256 current = allowance(owner, spender);
        if (current < value) revert ERC20InsufficientAllowance(spender, current, value);
        _approve(owner, spender, current - value, false);
    }
}
