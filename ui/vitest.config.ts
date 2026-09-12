import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import fs from "node:fs";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// 与 vite.config.mts 保持一致：企业版 UI 以 symlink 形式存在时走真实目录，否则走 fallback
const isEnterpriseBuild = fs.existsSync(path.join(__dirname, "app", "enterprise"));

export default defineConfig({
	// 测试会导入含 JSX 的视图模块，需要 react 插件做转换
	plugins: [react()],
	resolve: {
		alias: {
			"@": path.resolve(__dirname, "."),
			"@enterprise": isEnterpriseBuild
				? path.resolve(__dirname, "app", "enterprise")
				: path.resolve(__dirname, "app", "_fallbacks", "enterprise"),
		},
	},
	esbuild: {
		jsx: "automatic",
	},
	test: {
		globals: true,
	},
});