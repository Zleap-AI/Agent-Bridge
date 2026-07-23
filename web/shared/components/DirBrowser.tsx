/**
 * 目录浏览器组件
 *
 * 用于浏览本地文件系统，选择工作目录。支持盘符列表（Windows）、
 * 目录导航、上一级返回和目录选择。
 *
 * @file DirBrowser.tsx
 * @author Lzm
 * @date 2026-07-22
 *
 * @encoding UTF-8
 * @lang TypeScript 5.x / React 19.x (JSX)
 */

import { FileText, Folder } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useI18n } from "../i18n";

/* ─── 类型定义 ─────────────────────────────────────────────── */

export interface DirEntry {
  name: string;
  path: string;
  is_dir: boolean;
}

export interface DirBrowserProps {
  open: boolean;
  /**
   * 当用户选择一个目录时调用
   * @param path 选中的路径
   */
  onSelect: (path: string) => void;
  /** 关闭浏览器 */
  onClose: () => void;
}

/* ─── 组件 ─────────────────────────────────────────────────── */

export function DirBrowser({ open, onSelect, onClose }: DirBrowserProps) {
  const { t } = useI18n();
  const [loading, setLoading] = useState(false);
  const [currentPath, setCurrentPath] = useState("");
  const [entries, setEntries] = useState<DirEntry[]>([]);
  const [error, setError] = useState("");

  /**
   * 加载指定路径的目录列表
   */
  const loadDir = useCallback(async (path: string) => {
    setLoading(true);
    setError("");
    try {
      const res = await fetch("/api/v1/local/browse", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path }),
      });
      const data = await res.json();
      if (!res.ok) {
        setError(data?.error?.message || t("local.diagUnavailable"));
        return;
      }
      setCurrentPath(data.path);
      setEntries(data.entries || []);
    } catch (e) {
      setError(`读取目录失败: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, [t]);

  /**
   * 加载盘符列表（Windows）
   */
  const loadDrives = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const res = await fetch("/api/v1/local/browse/drives");
      const data = await res.json();
      if (!res.ok) {
        setError(data?.error?.message || "读取盘符失败");
        return;
      }
      const drives: string[] = data.drives || [];
      setCurrentPath("");
      setEntries(
        drives.map((d: string) => ({
          name: d,
          path: d,
          is_dir: true,
        }))
      );
    } catch (e) {
      setError(`读取盘符失败: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  // 打开时加载
  useEffect(() => {
    if (open) {
      if (currentPath) {
        loadDir(currentPath);
      } else {
        loadDrives();
      }
    }
  }, [open, currentPath, loadDir, loadDrives]);

  /**
   * 点击目录条目：进入下一级
   */
  const handleDirClick = useCallback(
    (entry: DirEntry) => {
      if (entry.is_dir) {
        loadDir(entry.path);
      }
    },
    [loadDir]
  );

  /**
   * 返回上一级
   */
  const handleGoUp = useCallback(() => {
    if (!currentPath) return;
    const parent = currentPath.replace(/[\\/]+$/, ""); // 去掉末尾分隔符
    const idx = Math.max(parent.lastIndexOf("\\"), parent.lastIndexOf("/"));
    if (idx <= 0) {
      // 已到根目录（如 D:\），回到盘符列表
      loadDrives();
    } else {
      let upPath = parent.substring(0, idx);
      // 如果为盘符（如 D:），补上反斜杠变为 D:\
      if (/^[A-Za-z]:$/.test(upPath)) {
        upPath += "\\";
      }
      loadDir(upPath);
    }
  }, [currentPath, loadDir, loadDrives]);

  /**
   * 选择当前目录
   */
  const handleSelectDir = useCallback(() => {
    if (currentPath) {
      onSelect(currentPath);
      onClose();
    }
  }, [currentPath, onSelect, onClose]);

  /**
   * 关闭时重置状态
   */
  const handleClose = useCallback(() => {
    setCurrentPath("");
    setEntries([]);
    setError("");
    onClose();
  }, [onClose]);

  if (!open) return null;

  return (
    <div className="dir-browser">
      <div className="dir-browser__header">
        <span className="dir-browser__path">
          {currentPath || "选择磁盘"}
        </span>
        <div className="dir-browser__actions">
          {currentPath && (
            <button
              className="button--tiny"
              onClick={handleGoUp}
              type="button"
            >
              上一级
            </button>
          )}
          <button
            className="button--tiny"
            onClick={handleClose}
            type="button"
          >
            取消
          </button>
        </div>
      </div>
      <div className="dir-browser__body">
        {loading ? (
          <div className="dir-browser__loading">加载中...</div>
        ) : error ? (
          <div className="dir-browser__error">{error}</div>
        ) : entries.length === 0 ? (
          <div className="dir-browser__empty">空目录</div>
        ) : (
          <ul className="dir-browser__list">
            {entries.map((entry) => (
              <li
                key={entry.path}
                className="dir-browser__item"
                onClick={() => handleDirClick(entry)}
                title={entry.path}
              >
                <span className="dir-browser__item-icon">
                  {entry.is_dir ? <Folder size={16} /> : <FileText size={16} />}
                </span>
                <span className="dir-browser__item-name">
                  {entry.name}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
      {currentPath && (
        <div className="dir-browser__footer">
          <button
            className="button button--primary"
            onClick={handleSelectDir}
            type="button"
          >
            选择此目录
          </button>
        </div>
      )}
    </div>
  );
}
