import { describe, it, expect, vi, beforeEach } from "vitest";
import { createElement } from "react";
import type { Dispatch, SetStateAction } from "react";
import { renderToString } from "react-dom/server";
// 避开真实 store / router 依赖，仅保留 LogDetailView 的纯渲染路径
vi.mock("@/app/workspace/dashboard/utils/chartUtils", () => ({
	formatCost: (v: number) => `$${v}`,
	formatLatency: (v: number) => `${v}ms`,
}));
vi.mock("@/components/ui/accordion", () => ({
	Accordion: ({ children }: any) => children,
	AccordionContent: ({ children }: any) => children,
	AccordionItem: ({ children }: any) => children,
	AccordionTrigger: ({ children }: any) => children,
}));
vi.mock("@/components/ui/alertDialog", () => ({
	AlertDialog: ({ children }: any) => children,
	AlertDialogAction: ({ children }: any) => children,
	AlertDialogCancel: ({ children }: any) => children,
	AlertDialogContent: ({ children }: any) => children,
	AlertDialogDescription: ({ children }: any) => children,
	AlertDialogFooter: ({ children }: any) => children,
	AlertDialogHeader: ({ children }: any) => children,
	AlertDialogTitle: ({ children }: any) => children,
	AlertDialogTrigger: ({ children }: any) => children,
}));
vi.mock("@/components/ui/badge", () => ({ Badge: ({ children }: any) => children }));
vi.mock("@/components/ui/button", () => ({ Button: ({ children }: any) => children }));
vi.mock("@/components/ui/codeEditor", () => ({ CodeEditor: () => null }));
vi.mock("@/components/ui/skeleton", () => ({
	Skeleton: (props: any) => {
		const { className } = props;
		return require("react").createElement("div", { "data-slot": "skeleton", className });
	},
}));
// nuqs 的 useQueryState 需要 adapter 上下文，SSR 测试用默认值 stub 替代
vi.mock("nuqs", () => ({
	parseAsBoolean: { withDefault: (d: boolean) => ({ withOptions: () => ({ defaultValue: d }) }) },
	parseAsString: { withOptions: () => ({}) },
	useQueryState: (_key: string, parser: any) => [parser?.defaultValue ?? null, vi.fn()],
}));
// renderToString 无法驱动 useState：mock 在渲染期按文本标签捕获菜单处理器，测试直接调用它们模拟用户交互
const dropdownRegistry = vi.hoisted(() => ({
	checkboxItems: [] as Array<{ label: string; checked: unknown; onCheckedChange: (checked: boolean) => void }>,
	items: [] as Array<{
		label: string;
		disabled?: boolean;
		onClick?: () => void;
		onSelect?: (e: { preventDefault: () => void }) => void;
	}>,
	onOpenChange: undefined as undefined | ((open: boolean) => void),
}));
vi.mock("@/components/ui/dropdownMenu", () => {
	const labelOf = (children: unknown): string =>
		require("react")
			.Children.toArray(children)
			.filter((c: unknown) => typeof c === "string" || typeof c === "number")
			.join("")
			.trim();
	return {
		DropdownMenu: ({ children, onOpenChange }: any) => {
			dropdownRegistry.onOpenChange = onOpenChange;
			return children;
		},
		DropdownMenuCheckboxItem: ({ children, checked, onCheckedChange }: any) => {
			dropdownRegistry.checkboxItems.push({ label: labelOf(children), checked, onCheckedChange });
			return children;
		},
		DropdownMenuContent: ({ children }: any) => children,
		DropdownMenuItem: ({ children, disabled, onClick, onSelect }: any) => {
			dropdownRegistry.items.push({ label: labelOf(children), disabled, onClick, onSelect });
			return children;
		},
		DropdownMenuSeparator: () => null,
		DropdownMenuTrigger: ({ children }: any) => children,
	};
});
vi.mock("@/components/ui/separator", () => ({ DottedSeparator: () => null }));
vi.mock("@/components/ui/switch", () => ({ Switch: () => null }));
vi.mock("@/components/ui/tabs", () => ({
	Tabs: ({ children }: any) => children,
	TabsContent: ({ children }: any) => children,
	TabsList: ({ children }: any) => children,
	TabsTrigger: ({ children }: any) => children,
}));
vi.mock("@/components/ui/tooltip", () => ({
	Tooltip: ({ children }: any) => children,
	TooltipContent: ({ children }: any) => children,
	TooltipTrigger: ({ children }: any) => children,
}));
vi.mock("@/components/ui/truncatedLabel", () => ({ TruncatedLabel: ({ children }: any) => children }));
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

import { LogDetailView, isRoleDraftDirty, toggleRoleInDraft } from "./logDetailView";
import type { LogEntry } from "@/lib/types/logs";
import type { MessageRole } from "./logDetailView";

function defaultRoleProps() {
	return {
		visibleRoles: new Set<MessageRole>(["user", "assistant"]),
		setVisibleRoles: (() => {}) as Dispatch<SetStateAction<Set<MessageRole>>>,
	};
}

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
			html = renderToString(
				createElement(LogDetailView, { log, isMetaDataLoading: true, resolvedSelectedPromptName: "resolved-name", ...defaultRoleProps() }),
			);
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
			html = renderToString(createElement(LogDetailView, { log, isMetaDataLoading: false, ...defaultRoleProps() }));
		}).not.toThrow();
		// isMetaDataLoading=false 时不应再展示骨架
		expect(html).not.toContain("logdetails-meta-skeleton");
		expect(html).not.toContain("logdetails-error-skeleton");
		// 差异字段区块需有可接受回退 UI，不得无边界空白
		// error_details 缺失但 status=error 时展示回退文案
		expect(html).toContain("The provider returned an error without a message.");
		// 列表行自带字段仍正常
		expect(html).toContain("gpt-4o-mini");
		expect(html).toContain("Request details");
		// 不应有整屏空白：至少包含 Details / Routing / Plugin 等 Tab 结构
		expect(html).toContain("Plugin Logs");
	});

	it("分页消息按最新消息优先展示", () => {
		const log = makeListRowFixture();
		const html = renderToString(
			createElement(LogDetailView, {
				log,
				paginatedMessages: [
					{ role: "assistant", content: "newer message" },
					{ role: "user", content: "older message" },
				],
				hasMoreMessages: false,
				...defaultRoleProps(),
			}),
		);

		expect(html.indexOf("newer message")).toBeLessThan(html.indexOf("older message"));
	});

	it("responses 推理碎片 role=assistant 不再渲染为空 Assistant 气泡", () => {
		const log = makeListRowFixture();
		const html = renderToString(
			createElement(LogDetailView, {
				log,
				paginatedMessages: [
					{ type: "message", role: "assistant", content: [{ type: "output_text", text: "real answer" }] },
					{ type: "reasoning", role: "assistant", content: [], summary: [] },
					{ type: "reasoning", content: [], summary: [] },
					{ type: "message", role: "assistant", content: [] },
				],
				hasMoreMessages: false,
				...defaultRoleProps(),
			}),
		);

		expect(html).toContain("real answer");
		expect(html).not.toContain(">assistant<");
	});

	it("developer 聊天消息按 system 角色过滤", () => {
		const log = makeListRowFixture();
		const renderWith = (roles: MessageRole[]) =>
			renderToString(
				createElement(LogDetailView, {
					log,
					paginatedMessages: [{ role: "developer", content: "sys prompt" }],
					hasMoreMessages: false,
					visibleRoles: new Set<MessageRole>(roles),
					setVisibleRoles: (() => {}) as Dispatch<SetStateAction<Set<MessageRole>>>,
				}),
			);

		expect(renderWith(["system"])).toContain("sys prompt");
		expect(renderWith(["user", "assistant"])).not.toContain("sys prompt");
	});
});

describe("LogDetailView 角色过滤草稿态", () => {
	beforeEach(() => {
		dropdownRegistry.checkboxItems = [];
		dropdownRegistry.items = [];
		dropdownRegistry.onOpenChange = undefined;
	});

	function renderRoleMenu(roles: MessageRole[], setVisibleRoles: Dispatch<SetStateAction<Set<MessageRole>>>) {
		return renderToString(
			createElement(LogDetailView, {
				log: makeListRowFixture(),
				paginatedMessages: [
					{ role: "assistant", content: "newer message" },
					{ role: "user", content: "older message" },
				],
				hasMoreMessages: false,
				visibleRoles: new Set<MessageRole>(roles),
				setVisibleRoles,
			}),
		);
	}

	function roleCheckbox(label: string) {
		const item = dropdownRegistry.checkboxItems.find((entry) => entry.label === label);
		expect(item).toBeDefined();
		return item!;
	}

	function menuItem(label: string) {
		const item = dropdownRegistry.items.find((entry) => entry.label === label);
		expect(item).toBeDefined();
		return item!;
	}

	it("勾选角色只改草稿：连续勾选 Reasoning、Tool 零请求，打开菜单重置也不提交", () => {
		const setVisibleRoles = vi.fn();
		renderRoleMenu(["user", "assistant"], setVisibleRoles as unknown as Dispatch<SetStateAction<Set<MessageRole>>>);
		roleCheckbox("Reasoning").onCheckedChange(true);
		roleCheckbox("Tool").onCheckedChange(true);
		dropdownRegistry.onOpenChange?.(true);
		expect(setVisibleRoles).not.toHaveBeenCalled();
	});

	it("Apply 一次提交合并集合", () => {
		// renderToString 无法驱动 useState，草稿初值即 committed：以合并集合为初值验证 Apply 的转发语义；
		// 勾选累积出合并草稿的过程由 toggleRoleInDraft 单测覆盖
		const combined: MessageRole[] = ["user", "assistant", "reasoning", "tool"];
		const setVisibleRoles = vi.fn();
		renderRoleMenu(combined, setVisibleRoles as unknown as Dispatch<SetStateAction<Set<MessageRole>>>);
		menuItem("Apply").onClick?.();
		expect(setVisibleRoles).toHaveBeenCalledTimes(1);
		expect(setVisibleRoles.mock.calls[0][0]).toEqual(new Set(combined));
	});

	it("Clear 只清空草稿不提交：保持菜单打开，Apply 才提交", () => {
		const setVisibleRoles = vi.fn();
		renderRoleMenu(["user", "assistant"], setVisibleRoles as unknown as Dispatch<SetStateAction<Set<MessageRole>>>);
		const clear = menuItem("Clear");
		const preventDefault = vi.fn();
		clear.onSelect?.({ preventDefault });
		expect(preventDefault).toHaveBeenCalledTimes(1);
		clear.onClick?.();
		expect(setVisibleRoles).not.toHaveBeenCalled();
		// Apply 没有 onSelect 拦截，点击即关闭菜单并提交
		expect(menuItem("Apply").onSelect).toBeUndefined();
	});

	it("草稿未改时 Apply 禁用", () => {
		const setVisibleRoles = vi.fn();
		renderRoleMenu(["user", "assistant"], setVisibleRoles as unknown as Dispatch<SetStateAction<Set<MessageRole>>>);
		expect(menuItem("Apply").disabled).toBe(true);
		expect(setVisibleRoles).not.toHaveBeenCalled();
	});

	it("toggleRoleInDraft 与 isRoleDraftDirty 语义", () => {
		let draft = new Set<MessageRole>(["user", "assistant"]);
		draft = toggleRoleInDraft(draft, "reasoning", true);
		draft = toggleRoleInDraft(draft, "tool", true);
		expect(draft).toEqual(new Set<MessageRole>(["user", "assistant", "reasoning", "tool"]));
		expect(toggleRoleInDraft(draft, "tool", false)).toEqual(new Set<MessageRole>(["user", "assistant", "reasoning"]));
		expect(isRoleDraftDirty(draft, new Set<MessageRole>(["user", "assistant"]))).toBe(true);
		expect(isRoleDraftDirty(new Set<MessageRole>(["user", "assistant"]), new Set<MessageRole>(["user", "assistant"]))).toBe(false);
		expect(isRoleDraftDirty(new Set<MessageRole>(), new Set<MessageRole>(["user", "assistant"]))).toBe(true);
	});
});