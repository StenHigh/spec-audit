<?php

declare(strict_types=1);

namespace Demo;

use DateTimeImmutable;
use Demo\Contracts\Notifier;

final class Service
{
    public function __construct(
        private FileStore $files,
        private DbStore $db,
        private Notifier $notifier,
        private UserRepository $users,
    ) {
    }

    public function run(?User $user, string $method, bool $useDb): string
    {
        $this->files->save('key', 'value');
        $this->db->save('key', 'value');
        $this->notifier->notify('saved');
        $found = $this->users->find(1);
        $store = $useDb ? $this->db : $this->files;
        $store->save('shared', 'value');
        $this->files->$method();
        $name = $user?->getName();
        $zero = Money::zero();
        $stamp = (new DateTimeImmutable())->format('Y');

        return ($name ?? 'anonymous') . '/' . ($found?->getName() ?? '-') . '/' . $zero->cents . '/' . $stamp;
    }
}
