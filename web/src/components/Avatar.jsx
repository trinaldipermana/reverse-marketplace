export default function Avatar({ initials, small = false }) {
  return <span className={`avatar ${small ? 'avatar-small' : ''}`}>{initials}</span>;
}
