-- Кабинет (task/ak-cabinet): привязка ещё одного входа к уже вошедшему пользователю (ADR-58).
-- Непустой link_user — вход начат из кабинета: внешний аккаунт привязывается к этому пользователю.
ALTER TABLE oauth_states ADD COLUMN link_user UUID REFERENCES users(id) ON DELETE CASCADE;
