import React, { useEffect, useState, useRef } from "react";

const Report = () => {
  const [tasks, setTasks] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [wsUpdate, setWsUpdate] = useState(null);
  const wsRef = useRef(null);

  useEffect(() => {
    fetch("http://localhost:8064/tasks")
      .then((res) => {
        if (!res.ok) throw new Error("Failed to fetch tasks");
        return res.json();
      })
      .then((data) => {
        setTasks(data.tasks || []);
        setLoading(false);
      })
      .catch((err) => {
        setError(err.message);
        setLoading(false);
      });
  }, []);

  // WebSocket for real-time updates
  useEffect(() => {
    const ws = new WebSocket("ws://localhost:8064/ws/updates");
    wsRef.current = ws;
    ws.onopen = () => {
      // Optionally: ws.send("hello");
    };
    ws.onmessage = (event) => {
      try {
        const update = JSON.parse(event.data);
        setWsUpdate(update);
        // Optionally: update tasks state here if needed
        // For now, just log it
        console.log("WebSocket update:", update);
      } catch (e) {
        console.error("WebSocket parse error", event.data);
      }
    };
    ws.onerror = (err) => {
      console.error("WebSocket error", err);
    };
    ws.onclose = () => {
      // Optionally: try to reconnect
    };
    return () => {
      ws.close();
    };
  }, []);

  if (loading) return <div>Loading tasks...</div>;
  if (error) return <div style={{ color: "red" }}>Error: {error}</div>;

  return (
    <div className="w-full">
      <h1 className="text-center text-2xl font-medium py-4">Task Report</h1>
      {wsUpdate && (
        <div className="bg-green-50 p-2 mb-4 border border-green-200">
          <strong className="text-sm font-medium">Live update received:</strong>
          <pre className="m-0 text-xs">{JSON.stringify(wsUpdate, null, 2)}</pre>
        </div>
      )}
      <table className="w-full border-collapse">
        <thead>
          <tr className="bg-gray-50">
            <th className="border border-gray-200 p-2 text-left text-sm font-medium">ID</th>
            <th className="border border-gray-200 p-2 text-left text-sm font-medium">Name</th>
            <th className="border border-gray-200 p-2 text-left text-sm font-medium">Status</th>
            <th className="border border-gray-200 p-2 text-left text-sm font-medium">Local Path</th>
            <th className="border border-gray-200 p-2 text-left text-sm font-medium">Created At</th>
            <th className="border border-gray-200 p-2 text-left text-sm font-medium">Updated At</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <tr key={task.id} className="hover:bg-gray-50">
              <td className="border border-gray-200 p-2 text-sm">{task.id}</td>
              <td className="border border-gray-200 p-2 text-sm">{task.name}</td>
              <td className="border border-gray-200 p-2 text-sm">{task.status}</td>
              <td className="border border-gray-200 p-2 text-sm">{task.local_path || ""}</td>
              <td className="border border-gray-200 p-2 text-sm">{task.created_at}</td>
              <td className="border border-gray-200 p-2 text-sm">{task.updated_at}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

export default Report;
