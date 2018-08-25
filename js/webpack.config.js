const path = require("path");
const webpack = require("webpack");
const UglifyJSPlugin = require("uglifyjs-webpack-plugin");
const pkg = require("../package.json");

module.exports = {
  mode: "production",
  entry: path.resolve(__dirname, "client", "entry-browser.js"),
  output: {
    filename: "bundle.js",
    path: path.resolve(__dirname, "build")
  },
  plugins: [
    new UglifyJSPlugin({ output: { comments: false } }),
    new webpack.BannerPlugin(
      `velox - v${pkg.version} - https://github.com/jpillora/velox` +
        "\nJaime Pillora <dev@jpillora.com> - MIT Copyright 2017"
    )
  ],
  module: {
    rules: [
      {
        test: /\.js$/,
        exclude: /(node_modules|bower_components)/,
        use: {
          loader: "babel-loader",
          options: {
            presets: ["env"]
          }
        }
      }
    ]
  }
};
