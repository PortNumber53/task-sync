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
    <div style={{ padding: 24 }}>
      <h1>Task Report</h1>
      {wsUpdate && (
        <div style={{ background: '#e0ffe0', padding: 10, marginBottom: 16, border: '1px solid #bada55' }}>
          <strong>Live update received:</strong>
          <pre style={{ margin: 0, fontSize: 12 }}>{JSON.stringify(wsUpdate, null, 2)}</pre>
        </div>
      )}
      <table style={{ width: "100%", borderCollapse: "collapse" }}>
        <thead>
          <tr>
            <th style={{ border: "1px solid #ccc", padding: 8 }}>ID</th>
            <th style={{ border: "1px solid #ccc", padding: 8 }}>Name</th>
            <th style={{ border: "1px solid #ccc", padding: 8 }}>Status</th>
            <th style={{ border: "1px solid #ccc", padding: 8 }}>Local Path</th>
            <th style={{ border: "1px solid #ccc", padding: 8 }}>Created At</th>
            <th style={{ border: "1px solid #ccc", padding: 8 }}>Updated At</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <tr key={task.id}>
              <td style={{ border: "1px solid #ccc", padding: 8 }}>{task.id}</td>
              <td style={{ border: "1px solid #ccc", padding: 8 }}>{task.name}</td>
              <td style={{ border: "1px solid #ccc", padding: 8 }}>{task.status}</td>
              <td style={{ border: "1px solid #ccc", padding: 8 }}>{task.local_path || ""}</td>
              <td style={{ border: "1px solid #ccc", padding: 8 }}>{task.created_at}</td>
              <td style={{ border: "1px solid #ccc", padding: 8 }}>{task.updated_at}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

export default Report;
