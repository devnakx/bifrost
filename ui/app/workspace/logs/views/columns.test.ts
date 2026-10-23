import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { createTable, flexRender, getCoreRowModel } from "@tanstack/react-table";

import type { LogEntry } from "@/lib/types/logs";

import { createColumns, getMessage } from "./columns";

describe("metadata cells", () => {
	it.each([
		[[{ id: "item_audio", index: 0 }], "item_audio"],
		[{ nested: { id: "item_audio" } }, "nested"],
		[false, "false"],
		[0, "0"],
		["custom value", "custom value"],
		[null, "-"],
		[undefined, "-"],
	])("renders JSON metadata safely: %j", (value, expected) => {
		const log = { id: "log", metadata: { realtime_input_items: value } } as unknown as LogEntry;
		const table = createTable({
			data: [log],
			columns: createColumns(() => {}, false, ["realtime_input_items"]),
			getCoreRowModel: getCoreRowModel(),
			state: {},
			onStateChange: () => {},
			renderFallbackValue: null,
		});
		const cell = table
			.getRowModel()
			.rows[0].getAllCells()
			.find((cell) => cell.column.id === "metadata_realtime_input_items")!;
		expect(renderToStaticMarkup(flexRender(cell.column.columnDef.cell, cell.getContext()))).toContain(expected);
	});
});

describe("getMessage", () => {
	it("shows N/A for a successful model retrieve, which has no message body", () => {
		const log = { object: "model_retrieve", status: "success" } as unknown as LogEntry;

		expect(getMessage(log)).toBe("N/A");
	});

	it("keeps the error summary for a failed model retrieve", () => {
		const log = {
			object: "model_retrieve",
			status: "error",
			content_summary: "invalid model: path delimiters and percent-encoding are not allowed",
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("invalid model: path delimiters and percent-encoding are not allowed");
	});

	it("returns EI realtime text from input history", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "user",
					content: [{ type: "text", text: "hello from the browser" }],
				},
			],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("User: hello from the browser");
	});

	it("returns LM realtime text from output message", () => {
		const log = {
			object: "realtime.turn",
			input_history: [],
			output_message: {
				role: "assistant",
				content: [{ type: "text", text: "hello from the model" }],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("Assistant: hello from the model");
	});
	it("returns split realtime text when both user and assistant are present", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "user",
					content: [{ type: "text", text: "who are you?" }],
				},
			],
			output_message: {
				role: "assistant",
				content: [{ type: "text", text: "I am the assistant." }],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("User: who are you?\nAssistant: I am the assistant.");
	});

	it("returns split realtime text including tool output", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "tool",
					content: [{ type: "text", text: '{"nextResponse":"tool result"}' }],
				},
				{
					role: "user",
					content: [{ type: "text", text: "who are you?" }],
				},
			],
			output_message: {
				role: "assistant",
				content: [{ type: "text", text: "I am the assistant." }],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe('Tool Result: {"nextResponse":"tool result"}\nUser: who are you?\nAssistant: I am the assistant.');
	});

	it("returns realtime assistant tool calls from output message", () => {
		const log = {
			object: "realtime.turn",
			input_history: [
				{
					role: "user",
					content: [{ type: "text", text: "show me a pastel palette" }],
				},
			],
			output_message: {
				role: "assistant",
				tool_calls: [
					{
						function: {
							name: "display_color_palette",
							arguments: '{"theme":"pastel"}',
						},
					},
				],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe('User: show me a pastel palette\nAssistant Tool Call: display_color_palette({"theme":"pastel"})');
	});

	it("returns list_preview over content_summary when both are present", () => {
		const log = {
			object: "response.stream",
			list_preview: "预览优先",
			content_summary: "已根据参考图生成",
			responses_input_history: [
				{ role: "user", content: "生成两张图像" },
				{ type: "custom_tool_call_output", call_id: "call_1", output: "Script completed Wall time 53.3 seconds" },
			],
			responses_output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "应被预览覆盖" }] }],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("预览优先");
	});

	it("falls back to content_summary when agent turn has no assistant text output", () => {
		const log = {
			object: "response.stream",
			content_summary: "补全这个女角色的全身三视图",
			responses_input_history: [
				{ role: "user", content: "补全这个女角色的全身三视图" },
				{ type: "function_call_output", call_id: "call_1", output: "No content" },
			],
			responses_output: [{ type: "function_call", name: "exec", call_id: "call_2" }],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("补全这个女角色的全身三视图");
	});

	it("prepends reasoning snippet when same last input but different reasoning", () => {
		// 推理回退统一占位：两行末条输入相同、reasoning 不同时均返回固定占位，不泄漏摘要
		const makeLog = (reasoningText: string) =>
			({
				object: "chat.completion",
				input_history: [{ role: "user", content: "我将按同一材质生成三视图" }],
				output_message: {
					role: "assistant",
					content: "",
					reasoning: reasoningText,
					tool_calls: [{ function: { name: "exec", arguments: "{}" } }],
				},
			}) as unknown as LogEntry;

		const first = getMessage(makeLog("**Processing image reference**"));
		const second = getMessage(makeLog("**Considering image generation**"));

		expect(first).toBe("Reasoning...");
		expect(second).toBe("Reasoning...");
		expect(first).toBe(second);
	});

	it("ignores empty reasoning and falls back to last input", () => {
		const log = {
			object: "chat.completion",
			input_history: [{ role: "user", content: "补全三视图" }],
			output_message: {
				role: "assistant",
				content: "",
				tool_calls: [{ function: { name: "exec", arguments: "{}" } }],
			},
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("补全三视图");
	});

	it("returns list_preview for responses requests", () => {
		const log = {
			object: "response.stream",
			list_preview: "preview",
			content_summary: "hi",
			responses_input_history: [{ role: "user", content: "hello" }],
			responses_output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "应被预览覆盖" }] }],
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("preview");
	});

	it("falls back to content_summary when list_preview is empty or absent", () => {
		const makeLog = (list_preview?: string) =>
			({
				object: "response.stream",
				list_preview,
				content_summary: "全量摘要回退",
				responses_input_history: [{ role: "user", content: "hello" }],
				responses_output: [{ type: "message", role: "assistant", content: [{ type: "output_text", text: "应被摘要覆盖" }] }],
			}) as unknown as LogEntry;

		expect(getMessage(makeLog(""))).toBe("全量摘要回退");
		expect(getMessage(makeLog("   "))).toBe("全量摘要回退");
		expect(getMessage(makeLog(undefined))).toBe("全量摘要回退");
	});
	it("returns current assistant output when last chat input is a tool message", () => {
		const log = {
			object: "chat.completion",
			input_history: [
				{ role: "user", content: "what is the weather?" },
				{ role: "tool", content: "sunny", tool_call_id: "call_1" },
			],
			output_message: { role: "assistant", content: "It is sunny." },
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("It is sunny.");
	});

	it("returns final assistant output for chat requests", () => {
		const log = {
			object: "chat.completion",
			input_history: [{ role: "user", content: "what is the weather?" }],
			output_message: { role: "assistant", content: "It is sunny." },
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("It is sunny.");
	});

	it("returns prompt for image edit requests", () => {
		const log = {
			object: "image_edit",
			image_edit_input: { prompt: "把背景换成海边", images: [{ image: null }] },
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("把背景换成海边");
	});

	it("falls back to last human chat input when output has no text", () => {
		const log = {
			object: "chat.completion",
			input_history: [
				{ role: "user", content: "what is the weather?" },
				{ role: "tool", content: "sunny", tool_call_id: "call_1" },
			],
			output_message: { role: "assistant", tool_calls: [{ function: { name: "get_weather", arguments: "{}" } }] },
		} as unknown as LogEntry;

		expect(getMessage(log)).toBe("what is the weather?");
	});
});