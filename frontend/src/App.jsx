import { BrowserRouter as Router, Routes, Route, Link } from 'react-router-dom';
import Report from './pages/Report';
import './App.css';

function App() {
  return (
    <Router>
      <nav style={{ padding: 16, borderBottom: '1px solid #eee' }}>
        <Link to="/report">Report</Link>
      </nav>
      <Routes>
        <Route path="/report" element={<Report />} />
        <Route path="*" element={<div style={{ padding: 24 }}><h2>Welcome to TaskSync</h2><p>Select "Report" to view all tasks.</p></div>} />
      </Routes>
    </Router>
  );
}

export default App
