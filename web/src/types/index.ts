// Wire types of the local API, one file per feature. internal/ui/wire_test.go checks them
// against the Go structs the server encodes.
export * from './workspace';
export * from './plans';
export * from './drift';
export * from './tools';
export * from './imports';
export * from './hub';
export * from './profiles';
export * from './graph';
export * from './compare';
export * from './history';
export * from './git';
export * from './discovery';
export * from './cost';
export * from './assist';
