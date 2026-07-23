/**
 * 通用 Markdown 渲染工具
 *
 * 提供简单的 Markdown 文本渲染（支持段落、代码块、内联代码、粗体、列表），
 * 以及工具调用详情折叠面板组件。
 *
 * @file renderMD.tsx
 * @author Lzm
 * @date 2026-07-22
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x (JSX)
 */

import React, { type ReactNode } from "react";

/**
 * renderMD 将简单 Markdown 文本渲染为 React 元素。
 *
 * 支持的语法：
 * - 段落（空行分隔）
 * - 代码块（``` 包裹）
 * - 内联代码（`code`）
 * - 粗体（**bold**）
 */
export function renderMD(text: string): ReactNode[] {
  const lines = text.split("\n");
  const elements: React.ReactNode[] = [];
  let inCodeBlock = false;
  let codeContent = "";
  let codeLang = "";

  lines.forEach((line, i) => {
    if (line.startsWith("```")) {
      if (inCodeBlock) {
        elements.push(
          <pre key={`code-${i}`}>
            <code>{codeContent}</code>
          </pre>
        );
        codeContent = "";
        codeLang = "";
      }
      inCodeBlock = !inCodeBlock;
      if (inCodeBlock) {
        codeLang = line.slice(3).trim();
      }
      return;
    }
    if (inCodeBlock) {
      codeContent += (codeContent ? "\n" : "") + line;
      return;
    }
    if (line.trim() === "") {
      elements.push(<br key={`br-${i}`} />);
      return;
    }
    // 内联代码处理
    const withCode = line.split(/(`[^`]+`)/).map((part, j) => {
      if (part.startsWith("`") && part.endsWith("`")) {
        return <code key={`${i}-${j}`}>{part.slice(1, -1)}</code>;
      }
      // 粗体处理
      const withBold = part.split(/(\*\*[^*]+\*\*)/).map((p, k) => {
        if (p.startsWith("**") && p.endsWith("**")) {
          return <strong key={`${i}-${j}-${k}`}>{p.slice(2, -2)}</strong>;
        }
        return p;
      });
      return <React.Fragment key={`${i}-${j}`}>{withBold}</React.Fragment>;
    });
    elements.push(<p key={`p-${i}`}>{withCode}</p>);
  });

  return elements;
}

/**
 * ToolCallDetails 工具调用详情折叠面板组件
 *
 * @param toolCall 工具调用对象（JSON 序列化显示）
 */
export function ToolCallDetails({
  toolCall,
  label,
}: {
  toolCall: unknown;
  label?: string;
}) {
  const toolCallStr = toolCall
    ? typeof toolCall === "string"
      ? toolCall
      : JSON.stringify(toolCall, null, 2)
    : "";

  if (!toolCallStr) return null;

  return (
    <details className="tool-call-details">
      <summary>{label || "工具调用详情"}</summary>
      <pre>{toolCallStr}</pre>
    </details>
  );
}

/**
 * MDContent 渲染 Markdown 文本为 React 元素并包裹容器
 */
export function MDContent({ text }: { text: string }) {
  return <div className="md-content">{renderMD(text)}</div>;
}
