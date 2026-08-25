import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import path from "path";
import { fileURLToPath } from "url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
	// 测试会导入含 JSX 的视图模块，需要 react 插件做转换
	plugins: [react()],
	resolve: {
		alias: {
			"@": path.resolve(__dirname, "."),
		},
	},
	esbuild: {
		jsx: "automatic",
	},
	test: {
		globals: true,
	},
});