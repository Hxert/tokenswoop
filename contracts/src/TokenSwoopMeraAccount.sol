// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";

interface IMeraReserve {
    function deposit(uint256 amount) external;
    function withdraw(uint256 amount) external;
}

/// @notice EIP-7702 execution for a Mera EOA. There are no platform spending keys.
/// @dev Only the EOA's own signature authorizes an operation. Targets are immutable;
/// no arbitrary calls, delegatecalls, token transfers or native-value spending exist.
contract TokenSwoopMeraAccount {
    using SafeERC20 for IERC20;

    address public immutable implementation = address(this);
    IERC20 public immutable paymentToken;
    address public immutable paymentContract;
    IMeraReserve public immutable reserveContract;
    bytes32 private constant SLOT = keccak256("tokenswoop.mera.account.storage.v1");
    bytes32 private constant DOMAIN_TYPE = keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");
    bytes32 private constant OP_TYPE = keccak256("WalletOperation(uint8 action,uint256 amount,bytes32 id,uint256 deadline,address implementation)");
    struct State { mapping(bytes32 => bool) used; bool executing; }

    error InvalidOperation();
    error InvalidSignature();
    error AlreadyExecuted();
    event OperationExecuted(bytes32 indexed id, uint8 action, uint256 amount);

    constructor(address token, address payment, address reserve) {
        if (token.code.length == 0 || payment.code.length == 0 || reserve.code.length == 0) revert InvalidOperation();
        paymentToken = IERC20(token);
        paymentContract = payment;
        reserveContract = IMeraReserve(reserve);
    }

    function _state() private pure returns (State storage state) {
        bytes32 slot = SLOT;
        assembly { state.slot := slot }
    }

    function operationUsed(bytes32 id) external view returns (bool) { return _state().used[id]; }

    function operationDigest(uint8 action, uint256 amount, bytes32 id, uint256 deadline) public view returns (bytes32) {
        bytes32 domain = keccak256(abi.encode(DOMAIN_TYPE, keccak256("TokenSwoop Mera"), keccak256("1"), block.chainid, address(this)));
        bytes32 operation = keccak256(abi.encode(OP_TYPE, action, amount, id, deadline, implementation));
        return keccak256(abi.encodePacked(hex"1901", domain, operation));
    }

    // 0: payment approve (zero revokes); 1: reserve approve; 2: deposit; 3: withdraw.
    function execute(uint8 action, uint256 amount, bytes32 id, uint256 deadline, bytes calldata signature) external {
        State storage state = _state();
        if (address(this) == implementation || state.executing || id == bytes32(0) || action > 3
            || deadline < block.timestamp || deadline > block.timestamp + 15 minutes
            || (action >= 2 && amount == 0) || (action == 0 && amount > 100_000_000)) revert InvalidOperation();
        if (state.used[id]) revert AlreadyExecuted();
        if (ECDSA.recover(operationDigest(action, amount, id, deadline), signature) != address(this)) revert InvalidSignature();
        state.used[id] = true;
        state.executing = true;
        if (action == 0) paymentToken.forceApprove(paymentContract, amount);
        else if (action == 1) paymentToken.forceApprove(address(reserveContract), amount);
        else if (action == 2) reserveContract.deposit(amount);
        else reserveContract.withdraw(amount);
        state.executing = false;
        emit OperationExecuted(id, action, amount);
    }

    // The original EOA key continues to authorize ordinary messages and contracts.
    function isValidSignature(bytes32 hash, bytes calldata signature) external view returns (bytes4) {
        (address signer, ECDSA.RecoverError err,) = ECDSA.tryRecover(hash, signature);
        return address(this) != implementation && err == ECDSA.RecoverError.NoError && signer == address(this)
            ? bytes4(0x1626ba7e) : bytes4(0xffffffff);
    }

    receive() external payable {}
}
