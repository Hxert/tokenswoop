// SPDX-License-Identifier: UNLICENSED
pragma solidity 0.8.37;

/// @notice Minimal ABI used by the reserve; no borrowing or credit delegation is exposed.
interface IAaveV3Pool {
    function supply(address asset, uint256 amount, address onBehalfOf, uint16 referralCode) external;
    function withdraw(address asset, uint256 amount, address to) external returns (uint256);
    function getReserveAToken(address asset) external view returns (address);
}

interface IAaveAToken {
    function POOL() external view returns (address);
    function UNDERLYING_ASSET_ADDRESS() external view returns (address);
}
