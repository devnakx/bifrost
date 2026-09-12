import { describe, it, expect, vi } from "vitest";
import { createElement } from "react";
import { renderToString } from "react-dom/server";
// 避开真实 store / router 依赖，仅保留 LogDetailView 的纯渲染路径
vi.mock("@/app/workspace/dashboard/utils/chartUtils", () => ({
	formatCost: (v: number) => `$${v}`,
	formatLatency: (v: number) => `${v}ms`,
}));
vi.mock("@/components/ui/accordion", () => ({ Accordion: ({children}:any)=> children, AccordionContent: ({children}:any)=> children, AccordionItem: ({children}:any)=> children, AccordionTrigger: ({children}:any)=> children }));
vi.mock("@/components/ui/alertDialog", () => ({ AlertDialog: ({children}:any)=> children, AlertDialogAction: ({children}:any)=> children, AlertDialogCancel: ({children}:any)=> children, AlertDialogContent: ({children}:any)=> children, AlertDialogDescription: ({children}:any)=> children, AlertDialogFooter: ({children}:any)=> children, AlertDialogHeader: ({children}:any)=> children, AlertDialogTitle: ({children}:any)=> children, AlertDialogTrigger: ({children}:any)=> children }));
vi.mock("@/components/ui/badge", () => ({ Badge: ({children}:any)=> children }));
vi.mock("@/components/ui/button", () => ({ Button: ({children}:any)=> children }));
vi.mock("@/components/ui/codeEditor", () => ({ CodeEditor: () => null }));
vi.mock("@/components/ui/skeleton", () => ({ Skeleton: (props:any) => { const {className}=props; return require("react").createElement("div", {"data-slot":"skeleton", className}); } }));
vi.mock("@/components/ui/dropdownMenu", () => ({ DropdownMenu: ({children}:any)=> children, DropdownMenuCheckboxItem: ({children}:any)=> children, DropdownMenuContent: ({children}:any)=> children, DropdownMenuItem: ({children}:any)=> children, DropdownMenuSeparator: () => null, DropdownMenuTrigger: ({children}:any)=> children }));
vi.mock("@/components/ui/separator", () => ({ DottedSeparator: () => null }));
vi.mock("@/components/ui/switch", () => ({ Switch: () => null }));
vi.mock("@/components/ui/tabs", () => ({ Tabs: ({children}:any)=> children, TabsContent: ({children}:any)=> children, TabsList: ({children}:any)=> children, TabsTrigger: ({children}:any)=> children }));
vi.mock("@/components/ui/tooltip", () => ({ Tooltip: ({children}:any)=> children, TooltipContent: ({children}:any)=> children, TooltipTrigger: ({children}:any)=> children }));
vi.mock("@/components/ui/truncatedLabel", () => ({ TruncatedLabel: ({children}:any)=> children }));
vi.mock("@/lib/store", () => ({
	useGetUserAgentMappingsQuery: () => ({ data: { mappings: [] } }),
	useGetProvidersQuery: () => ({ data: undefined, isLoading: false, isError: true }),
}));
vi.mock("@/lib/store/apis/logsApi", () => ({
	useLazyGetLogRawQuery: () => [vi.fn(), { data: undefined, isFetching: false }],
	useLazyGetLogMessagesPageQuery: () => [vi.fn(), {}],
}));
vi.mock("@/hooks/useCopyToClipboard", () => ({
	useCopyToClipboard: () => ({ copy: async () => {} }),
}));
vi.mock("@tanstack/react-router", () => ({
	Link: ({ children }: { children: unknown }) => children,
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { LogDetailView } from "./logDetailView";
import type { LogEntry } from "@/lib/types/logs";

// 列表行 shape——仅含 baseCols 预览列，缺失 error_details / params / plugin_logs 等差异字段
function makeListRowFixture(overrides: Partial<LogEntry> = {}): LogEntry {
	return {
		id: "log-list-row-001",
		object: "chat.completion",
		provider: "openai",
		model: "gpt-4o-mini",
		timestamp: new Date("2026-08-26T08:00:00.000Z").toISOString(),
		created_at: new Date("2026-08-26T08:00:00.000Z").toISOString(),
		input_history: [{ role: "user", content: "hello" } as never],
		status: "success",
		stream: false,
		latency: 123,
		number_of_retries: 0,
		fallback_index: 0,
		cost: 0.001,
		token_usage: { prompt_tokens: 10, completion_tokens: 20, total_tokens: 30 },
		selected_prompt_name: "list-prompt-name",
		content_hidden: false,
		has_object: true,
		...overrides,
	} as unknown as LogEntry;
}

describe("LogDetailView 去门控骨架屏", () => {
	it("档1 加载中：列表行 shape + isMetaDataLoading=true 不抛错且骨架可见、列表行自带字段正常显示", () => {
		const log = makeListRowFixture();
		let html = "";
		expect(() => {
			html = renderToString(createElement(LogDetailView, { log, isMetaDataLoading: true, resolvedSelectedPromptName: "resolved-name" }));
		}).not.toThrow();
		// 骨架屏：差异字段区块
		expect(html).toContain("logdetails-meta-skeleton");
		expect(html).toContain("logdetails-routing-skeleton");
		expect(html).toContain("logdetails-plugin-skeleton");
		// 列表行自带字段正常显示（不被门控）
		expect(html).toContain("gpt-4o-mini");
		expect(html).toContain("openai");
		expect(html).toContain("Request details");
		// 已有列表行回退：selected_prompt_name
		expect(html).toContain("resolved-name");
	});

	it("档2 isError 形态：列表行 shape + isError（isFullDataReady=true、isMetaDataLoading=false）不抛错且差异字段区块有可接受回退 UI", () => {
		const log = makeListRowFixture({ status: "error", error_details: undefined as never });
		let html = "";
		expect(() => {
			html = renderToString(createElement(LogDetailView, { log, isMetaDataLoading: false }));
		}).not.toThrow();
		// isMetaDataLoading=false 时不应再展示骨架
		expect(html).not.toContain("logdetails-meta-skeleton");
		expect(html).not.toContain("logdetails-error-skeleton");
		// 差异字段区块需有可接受回退 UI，不得无边界空白
		// error_details 缺失但 status=error 时展示回退文案
		expect(html).toContain("No error details available");
		// 列表行自带字段仍正常
		expect(html).toContain("gpt-4o-mini");
		expect(html).toContain("Request details");
		// 不应有整屏空白：至少包含 Details / Routing / Plugin 等 Tab 结构
		expect(html).toContain("Plugin Logs");
	});
});
