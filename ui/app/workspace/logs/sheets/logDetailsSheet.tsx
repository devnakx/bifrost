import { SheetNavigationButtons } from "@/components/sheetNavigationButtons";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { logsApi, useGetLogByIdQuery, useGetLogMessagesInfiniteQuery } from "@/lib/store/apis/logsApi";
import { useGetPromptQuery } from "@/lib/store/apis/promptsApi";
import { useAppDispatch } from "@/lib/store/hooks";
import type { LogEntry } from "@/lib/types/logs";
import { useSheetNavigation } from "@/hooks/useSheetNavigation";
import { Loader2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { LogDetailView } from "./logDetailView";

interface LogDetailSheetProps {
	log: LogEntry | null;
	open: boolean;
	onOpenChange: (open: boolean) => void;
	handleDelete?: (log: LogEntry) => void;
	onNavigate?: (direction: "prev" | "next") => void;
	hasPrev?: boolean;
	hasNext?: boolean;
	canReveal?: boolean;
	onViewSession?: (sessionId: string, logId: string) => void;
	onFilterByParentRequestId?: (parentRequestId: string) => void;
	onFilterBySessionId?: (sessionId: string) => void;
}

export function LogDetailSheet({
	log,
	open,
	onOpenChange,
	handleDelete,
	onNavigate,
	hasPrev = false,
	hasNext = false,
	canReveal = false,
	onViewSession,
	onFilterByParentRequestId,
	onFilterBySessionId,
}: LogDetailSheetProps) {
	const dispatch = useAppDispatch();
	const [pollingInterval, setPollingInterval] = useState(0);
	const {
		data: fullLog,
		isLoading,
		isError,
	} = useGetLogByIdQuery(log?.id ?? "", {
		skip: !open || !log?.id,
		pollingInterval,
	});

	// 首屏消息分页（offset=0, limit=20）- 随 open/id 变化自动请求
	const {
		data: messagesData,
		fetchNextPage,
		hasNextPage,
		isFetchingNextPage,
	} = useGetLogMessagesInfiniteQuery(log?.id ?? "", {
		skip: !open || !log?.id,
	});

	const flatMessages = useMemo(() => {
		if (!messagesData?.pages) return [];
		return messagesData.pages.flatMap((p) => p.messages);
	}, [messagesData]);

	const shouldPoll = isError || fullLog?.status === "processing";

	const isFullDataReady = log != null && (isError || (fullLog?.id === log.id && !isLoading));
	// Prefer full log when loaded; otherwise list row — enables prompt fetch in parallel with getLogById
	const selectedPromptId = log ? (fullLog?.id === log.id ? fullLog : log).selected_prompt_id : undefined;
	const { data: selectedPromptData } = useGetPromptQuery(selectedPromptId ?? "", {
		skip: !open || !selectedPromptId,
	});

	useEffect(() => {
		setPollingInterval(shouldPoll ? 2000 : 0);
	}, [shouldPoll]);

	// 轮询衔接：status=processing 时每次元数据刷新后 invalidate messages 缓存，使首页随流式进度刷新
	useEffect(() => {
		if (!open || !log?.id) return;
		if (fullLog?.status === "processing") {
			dispatch(logsApi.util.invalidateTags([{ type: "LogMessages", id: log.id }]));
		}
	}, [fullLog, open, log?.id, dispatch]);

	// 滚动触底加载下一页：IntersectionObserver 监听 sentinel
	const viewportRef = useRef<HTMLDivElement>(null);
	const sentinelRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		const viewport = viewportRef.current;
		const sentinel = sentinelRef.current;
		if (!viewport || !sentinel) return;
		if (!hasNextPage) return;
		const observer = new IntersectionObserver(
			(entries) => {
				if (entries[0].isIntersecting && hasNextPage && !isFetchingNextPage) {
					fetchNextPage();
				}
			},
			{ root: viewport, rootMargin: "100px" },
		);
		observer.observe(sentinel);
		return () => observer.disconnect();
	}, [hasNextPage, isFetchingNextPage, fetchNextPage]);

	// Keyboard navigation: arrow up/down to navigate between logs
	const { prev: prevKeys, next: nextKeys } = useSheetNavigation({
		enabled: open,
		hasPrev,
		hasNext,
		onNavigate: (direction) => onNavigate?.(direction),
	});

	if (!log) return null;

	const displayLog: LogEntry = isFullDataReady && fullLog ? fullLog : log;
	const resolvedSelectedPromptName = selectedPromptData?.prompt?.name ?? displayLog.selected_prompt_name ?? "";

	return (
		<Sheet open={open} onOpenChange={onOpenChange}>
			<SheetContent data-testid="log-detail-sheet" className="border-secondary flex w-full flex-col gap-4 overflow-hidden border p-4 sm:max-w-[60%] md:p-8">
				<SheetTitle className="sr-only">Log details</SheetTitle>
				<SheetDescription className="sr-only">
					{isFullDataReady && displayLog ? `Details for log ${displayLog.id}` : "Log details"}
				</SheetDescription>
				<ScrollArea viewportRef={viewportRef} className="flex min-h-0 flex-1 flex-col" viewportClassName="flex-1 [&>div]:!block [&>div]:!min-w-0">
					<LogDetailView
						log={displayLog}
						resolvedSelectedPromptName={resolvedSelectedPromptName}
						isMetaDataLoading={!isFullDataReady}
						handleDelete={handleDelete}
						canReveal={canReveal}
						onClose={() => onOpenChange(false)}
						onFilterByParentRequestId={onFilterByParentRequestId}
						paginatedMessages={flatMessages}
						hasMoreMessages={Boolean(hasNextPage)}
						isFetchingMoreMessages={isFetchingNextPage}
						sentinelRef={sentinelRef as React.RefObject<HTMLDivElement>}
						onLoadMore={() => {
							if (hasNextPage && !isFetchingNextPage) fetchNextPage();
						}}
						onFilterBySessionId={onFilterBySessionId}
						headerAction={
							<>
								{displayLog.parent_request_id && onViewSession ? (
									<Button
										variant="outline"
										size="sm"
										data-testid="session-button-view"
										onClick={() => onViewSession(displayLog.parent_request_id as string, displayLog.id)}
									>
										View Session
									</Button>
								) : null}
								<SheetNavigationButtons
									hasPrev={hasPrev}
									hasNext={hasNext}
									onNavigate={(dir) => onNavigate?.(dir)}
									prevKeys={prevKeys}
									nextKeys={nextKeys}
									entityLabel="log"
								/>
							</>
						}
					/>
					<div ref={sentinelRef} className="h-px w-full shrink-0" />
					{isFetchingNextPage ? (
						<div className="flex justify-center py-3">
							<Loader2 className="text-muted-foreground h-4 w-4 animate-spin" />
						</div>
					) : null}
				</ScrollArea>
			</SheetContent>
		</Sheet>
	);
}
