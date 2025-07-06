import React, { useEffect, useState } from "react";

const Report = () => {
  const [tasks, setTasks] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

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

  if (loading) return <div>Loading tasks...</div>;
  if (error) return <div style={{ color: "red" }}>Error: {error}</div>;

  return (
    <div style={{ padding: 24 }}>
      <h1>Task Report</h1>
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
