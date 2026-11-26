"use strict";

const path = require("node:path");
const { signAsync } = require("@electron/osx-sign");

const identifiers = {
  "nori-connector": "com.nori.mac.connector",
  "nori-connector-keychain": "com.nori.mac.keychain",
  "nori-desktop-service": "com.nori.mac.service",
};

module.exports = async function signNori(options) {
  const defaults = options.optionsForFile;
  const nativeDirectory = path.join(options.app, "Contents", "MacOS");
  await signAsync({
    ...options,
    optionsForFile(filePath) {
      const configured = defaults ? defaults(filePath) : {};
      const identifier = path.dirname(filePath) === nativeDirectory && identifiers[path.basename(filePath)];
      if (!identifier) return configured;
      return {
        ...configured,
        additionalArguments: [...(configured.additionalArguments || []), "--identifier", identifier],
      };
    },
  });
};
