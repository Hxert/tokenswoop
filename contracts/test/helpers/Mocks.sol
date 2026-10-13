// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {IERC1271} from "@openzeppelin/contracts/interfaces/IERC1271.sol";
import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";
import {TokenSwoopPayG} from "../../src/TokenSwoopPayG.sol";
import {TokenSwoopPayGBatch} from "../../src/TokenSwoopPayGBatch.sol";

contract ContractWallet is IERC1271 {
    address public immutable signer;
    bool public acceptsSignatures = true;

    constructor(address signer_) {
        signer = signer_;
    }

    function setAcceptsSignatures(bool accepts) external {
        acceptsSignatures = accepts;
    }

    function isValidSignature(bytes32 digest, bytes memory signature) external view returns (bytes4) {
        if (acceptsSignatures && ECDSA.recover(digest, signature) == signer) return IERC1271.isValidSignature.selector;
        return 0xffffffff;
    }
}

contract TestPaymentToken is ERC20 {
    uint8 private immutable _decimals;
    bool public failTransfers;
    TokenSwoopPayG public reentryTarget;
    bool public attemptedReentry;
    bool public reentrySucceeded;
    bool public reenterBatch;
    uint256 public transferFromCalls;

    constructor(uint8 decimals_) ERC20("Test", "TEST") {
        _decimals = decimals_;
    }

    function decimals() public view override returns (uint8) {
        return _decimals;
    }

    function mint(address to, uint256 amount) external {
        _mint(to, amount);
    }

    function configure(bool fail, TokenSwoopPayG target) external {
        failTransfers = fail;
        reentryTarget = target;
    }

    function configureBatchReentry(TokenSwoopPayG target) external {
        reentryTarget = target;
        reenterBatch = true;
    }

    function transferFrom(address from, address to, uint256 value) public override returns (bool) {
        ++transferFromCalls;
        if (failTransfers) return false;
        if (!attemptedReentry && address(reentryTarget) != address(0)) {
            attemptedReentry = true;
            if (reenterBatch) {
                try TokenSwoopPayGBatch(address(reentryTarget)).chargeBatch(from, keccak256("reentrant-batch"), value) {
                    reentrySucceeded = true;
                } catch {}
            } else {
                try reentryTarget.charge(from, keccak256("reentrant"), value) {
                    reentrySucceeded = true;
                } catch {}
            }
        }
        return super.transferFrom(from, to, value);
    }
}

contract NoMetadataToken {}
