import React, { useEffect, useState, useCallback } from "react";
import { useParams, Link } from "react-router-dom";
import { API_BASE_URL } from "../config";

const Node = ({ node, prefix = "", isLast = true }) => {
  const connector = prefix ? (isLast ? "└─ " : "├─ ") : "";
  const nextPrefix = prefix + (isLast ? "   " : "│  ");
  const children = node.children || [];

  // Parse results JSON if present; some steps may have a JSON string mapping name->text
  let parsed = null;
  if (node.results) {
    try { parsed = JSON.parse(node.results); } catch (e) { parsed = null; }
  }
  const hasResults = !!parsed || !!node.results;
  const isRubric = /rubric_shell/i.test(String(node.title || ""));

  // Derive emoji status for columns: 1..4, O, G
  const expectedKeys = [
    "solution1.patch", "solution2.patch", "solution3.patch", "solution4.patch", "original", "golden.patch"
  ];
  function statusFromText(s) {
    if (!s) return "◦";
    const S = String(s);
    if (S.includes("✅") || /\bPASS\b/i.test(S) || /\bSUCCESS\b/i.test(S)) return "✅";
    if (S.includes("❌") || /\bFAIL\b/i.test(S) || /\bERROR\b/i.test(S)) return "❌";
    if (S.includes("⏭") || /\bSKIP\b/i.test(S)) return "⏭";
    if (/\bPENDING\b|\bRUNNING\b|\bQUEUED\b/i.test(S)) return "⏳";
    return "◦";
  }
  function computeIcons() {
    if (!hasResults) return ['·','·','·','·','·','·'];
    const getVal = (key) => {
      if (parsed && typeof parsed === 'object' && parsed !== null && key in parsed) return parsed[key];
      // heuristic fallback: try to find blocks labeled with key inside the raw string
      const raw = String(node.results || "");
      const idx = raw.indexOf(key);
      if (idx !== -1) {
        // take a small window around to infer status
        const window = raw.slice(idx, Math.min(raw.length, idx + 160));
        return window;
      }
      return null;
    };
    const arr = expectedKeys.map((k) => statusFromText(getVal(k)));
    // Guarantee fixed length
    while (arr.length < 6) arr.push('·');
    return arr;
  }
  const icons = computeIcons();

  return (
    <div className="my-1">
      {isRubric && (
        <div className="font-mono text-sm whitespace-pre text-primary flex items-center gap-2 text-left">
          <span className="text-gray-400">{prefix + connector}</span>
          {/* Reserve exact title width to align emoji columns */}
          <span className="invisible">{node.title}</span>
          <span className="ml-2 flex gap-0 select-none">
            {["1","2","3","4","O","G"].map((lbl, i) => (
              <span key={i} className="inline-block w-6 text-center leading-none text-sm text-gray-500 font-mono">{lbl}</span>
            ))}
          </span>
        </div>
      )}
      <div className="font-mono text-sm whitespace-pre text-primary flex items-center gap-2 text-left">
        <span className="text-gray-400">{prefix + connector}</span>
        <span className="font-mono">{node.title}</span>
        <span className="ml-2 flex gap-0">
          {icons.map((ic, i) => (
            <span key={i} className="inline-block w-6 text-center leading-none text-sm font-mono">{ic}</span>
          ))}
        </span>
      </div>
      {children.length > 0 && (
        <div className="mt-0.5">
          {children.map((child, idx) => (
            <Node
              key={child.id}
              node={child}
              prefix={nextPrefix}
              isLast={idx === children.length - 1}
            />
          ))}
        </div>
      )}
    </div>
  );
};

const TaskDetail = () => {
  const { id } = useParams();
  const [report, setReport] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  const fetchReport = useCallback(() => {
    setLoading(true);
    setError(null);
    fetch(`${API_BASE_URL}/tasks/${id}/report`, {
      method: "GET",
      headers: { Accept: "application/json" },
      mode: "cors",
    })
      .then(async (res) => {
        if (!res.ok) {
          const t = await res.text();
          throw new Error(`HTTP ${res.status}: ${t}`);
        }
        return res.json();
      })
      .then((data) => {
        setReport(data);
        setLoading(false);
      })
      .catch((err) => {
        setError(err.message);
        setLoading(false);
      });
  }, [id]);

  useEffect(() => {
    fetchReport();
  }, [fetchReport]);

  if (loading) return <div className="p-4">Loading report...</div>;
  if (error) return <div className="p-4 text-red-600">Error: {error}</div>;
  if (!report) return <div className="p-4">No report data.</div>;

  return (
    <div className="p-4">
      <div className="flex items-center justify-between mb-4">
        <div>
          <h2 className="text-2xl font-semibold">Task {report.task_id}: {report.task_name}</h2>
          <div className="text-sm text-gray-600">Output sizes: {Object.keys(report.output_sizes || {}).length} entries</div>
        </div>
        <div className="flex gap-2">
          <button onClick={fetchReport} className="px-3 py-1 text-sm bg-blue-600 text-white rounded hover:bg-blue-700">Refresh</button>
          <Link to="/report" className="px-3 py-1 text-sm bg-gray-100 rounded hover:bg-gray-200">Back</Link>
        </div>
      </div>
      <div className="text-xs text-gray-500 mb-1">Dependency tree</div>
      <div className="font-mono text-xs text-gray-600 mb-2">
        <span className="opacity-70">Legend:</span> <span className="ml-2">1 2 3 4 O G</span>
      </div>
      <div>
        {(report.roots || []).length === 0 ? (
          <div className="text-sm text-gray-600">No steps found.</div>
        ) : (
          report.roots.map((root, idx) => (
            <Node key={root.id} node={root} prefix="" isLast={idx === (report.roots.length - 1)} />
          ))
        )}
      </div>
    </div>
  );
};

export default TaskDetail;
